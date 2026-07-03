/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/pflag"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/pkg/version"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	capiflags "sigs.k8s.io/cluster-api/util/flags"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/controller"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/index"
	nb "github.com/evenh/cluster-api-ipam-provider-netbox/internal/netbox"
	ipamutil "github.com/evenh/cluster-api-ipam-provider-netbox/pkg/ipamutil"
	"github.com/evenh/cluster-api-ipam-provider-netbox/pkg/reconcileutil"
)

const (
	defaultMetricsAddr = "0"
	defaultProbeAddr   = ":8081"
)

type managerConfig struct {
	metricsAddr          string
	webhookCertPath      string
	webhookCertName      string
	webhookCertKey       string
	enableLeaderElection bool
	probeAddr            string
	watchNamespace       string
	watchFilter          string
	enableHTTP2          bool
	netboxRequestTimeout time.Duration
	managerOptions       capiflags.ManagerOptions
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2) //nolint:mnd // 2 is the conventional exit code for CLI usage errors.
	}

	ctrl.SetLogger(klog.Background())
	setupLog := ctrl.Log.WithName("setup")

	scheme, err := newScheme()
	if err != nil {
		setupLog.Error(err, "Failed to build scheme")
		os.Exit(1)
	}

	tlsOpts := makeTLSOptions(cfg.enableHTTP2)
	webhookServerOptions := newWebhookServerOptions(cfg, tlsOpts)
	managerOpts := newManagerOptions(cfg, scheme, webhookServerOptions)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), managerOpts)
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	if err = index.SetupIndexes(ctx, mgr); err != nil {
		setupLog.Error(err, "Failed to set up indexes")
		os.Exit(1)
	}
	if err = setupControllers(ctx, mgr, cfg.watchFilter, cfg.netboxRequestTimeout); err != nil {
		setupLog.Error(err, "Failed to set up controllers")
		os.Exit(1)
	}
	if err = addProbes(mgr); err != nil {
		setupLog.Error(err, "Failed to set up probes")
		os.Exit(1)
	}

	setupLog.Info("Starting manager", "version", version.Get().String())
	if err = mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

func parseFlags(args []string) (managerConfig, error) {
	return parseFlagsWithGoFlagSet(args, flag.CommandLine)
}

func parseFlagsWithGoFlagSet(args []string, goFlagSet *flag.FlagSet) (managerConfig, error) {
	cfg := managerConfig{
		metricsAddr:          defaultMetricsAddr,
		probeAddr:            defaultProbeAddr,
		webhookCertName:      "tls.crt",
		webhookCertKey:       "tls.key",
		netboxRequestTimeout: nb.DefaultRequestTimeout,
		managerOptions:       capiflags.ManagerOptions{},
	}

	flagSet := pflag.NewFlagSet("manager", pflag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	capiflags.AddManagerOptions(flagSet, &cfg.managerOptions)
	flagSet.AddGoFlagSet(goFlagSet)
	flagSet.StringVar(
		&cfg.metricsAddr,
		"metrics-bind-address",
		defaultMetricsAddr,
		"Metrics bind address. Use 0 to disable.",
	)
	flagSet.StringVar(&cfg.probeAddr, "health-probe-bind-address", defaultProbeAddr, "Health probe bind address.")
	flagSet.BoolVar(&cfg.enableLeaderElection, "leader-elect", false, "Enable leader election.")
	flagSet.StringVar(&cfg.watchNamespace, "namespace", "", "Namespace to watch. Empty means all namespaces.")
	flagSet.StringVar(&cfg.watchFilter, "watch-filter", "", "Cluster API watch filter label value.")
	flagSet.StringVar(&cfg.webhookCertPath, "webhook-cert-path", "", "Webhook certificate directory.")
	flagSet.StringVar(&cfg.webhookCertName, "webhook-cert-name", cfg.webhookCertName, "Webhook certificate file.")
	flagSet.StringVar(&cfg.webhookCertKey, "webhook-cert-key", cfg.webhookCertKey, "Webhook private key file.")
	flagSet.BoolVar(&cfg.enableHTTP2, "enable-http2", false, "Enable HTTP/2 for metrics and webhooks.")
	flagSet.DurationVar(
		&cfg.netboxRequestTimeout,
		"netbox-request-timeout",
		nb.DefaultRequestTimeout,
		"Timeout for individual NetBox API requests.",
	)

	if err := flagSet.Parse(args); err != nil {
		return managerConfig{}, fmt.Errorf("parse flags: %w", err)
	}
	if cfg.netboxRequestTimeout <= 0 {
		return managerConfig{}, fmt.Errorf("netbox-request-timeout must be positive, got %s", cfg.netboxRequestTimeout)
	}

	return cfg, nil
}

func newScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		clusterv1.AddToScheme,
		ipamv1.AddToScheme,
		ipamv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, fmt.Errorf("add types to scheme: %w", err)
		}
	}
	return scheme, nil
}

func makeTLSOptions(enableHTTP2 bool) []func(*tls.Config) {
	if enableHTTP2 {
		return nil
	}

	return []func(*tls.Config){
		func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		},
	}
}

func newWebhookServerOptions(cfg managerConfig, tlsOpts []func(*tls.Config)) webhook.Options {
	webhookServerOptions := webhook.Options{TLSOpts: tlsOpts}
	if cfg.webhookCertPath != "" {
		webhookServerOptions.CertDir = cfg.webhookCertPath
		webhookServerOptions.CertName = cfg.webhookCertName
		webhookServerOptions.KeyName = cfg.webhookCertKey
	}

	return webhookServerOptions
}

func newManagerOptions(
	cfg managerConfig,
	scheme *runtime.Scheme,
	webhookServerOptions webhook.Options,
) ctrl.Options {
	metricsServerOptions := metricsserver.Options{
		BindAddress: cfg.metricsAddr,
	}

	options := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhook.NewServer(webhookServerOptions),
		HealthProbeBindAddress: cfg.probeAddr,
		LeaderElection:         cfg.enableLeaderElection,
		LeaderElectionID:       "cluster-api-ipam-provider-netbox.ipam.cluster.x-k8s.io",
	}
	if cfg.watchNamespace != "" {
		options.Cache = cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				cfg.watchNamespace: {},
			},
		}
	}

	return options
}

func setupControllers(
	ctx context.Context,
	mgr ctrl.Manager,
	watchFilter string,
	netboxRequestTimeout time.Duration,
) error {
	if err := (&ipamutil.ClaimReconciler{
		ControllerBase: reconcileutil.ControllerBase{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorder("ipaddressclaim"),
		},
		WatchFilterValue: watchFilter,
		Adapter:          &controller.NetBoxProviderAdapter{RequestTimeout: netboxRequestTimeout},
	}).SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("create IPAddressClaim reconciler: %w", err)
	}

	if err := (&controller.NetBoxIPPoolReconciler{
		ControllerBase: reconcileutil.ControllerBase{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorder("netboxippool"),
		},
		WatchFilterValue: watchFilter,
		RequestTimeout:   netboxRequestTimeout,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("create NetBoxIPPool reconciler: %w", err)
	}

	if err := (&controller.GlobalNetBoxIPPoolReconciler{
		ControllerBase: reconcileutil.ControllerBase{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorder("globalnetboxippool"),
		},
		WatchFilterValue: watchFilter,
		RequestTimeout:   netboxRequestTimeout,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("create GlobalNetBoxIPPool reconciler: %w", err)
	}

	return nil
}

func addProbes(mgr ctrl.Manager) error {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("set up ready check: %w", err)
	}

	return nil
}
