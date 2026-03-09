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
	"crypto/tls"
	"flag"
	"os"

	"github.com/spf13/pflag"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/pkg/version"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ipamv1 "sigs.k8s.io/cluster-api/api/ipam/v1beta2"
	capiflags "sigs.k8s.io/cluster-api/util/flags"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/controller"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/index"
	ipamutil "github.com/evenh/cluster-api-ipam-provider-netbox/pkg/ipamutil"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(ipamv1.AddToScheme(scheme))
	utilruntime.Must(ipamv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		metricsCertPath      string
		metricsCertName      string
		metricsCertKey       string
		webhookCertPath      string
		webhookCertName      string
		webhookCertKey       string
		enableLeaderElection bool
		probeAddr            string
		watchNamespace       string
		watchFilter          string
		secureMetrics        bool
		enableHTTP2          bool
		tlsOpts              []func(*tls.Config)
		managerOptions       = capiflags.ManagerOptions{}
	)

	capiflags.AddManagerOptions(pflag.CommandLine, &managerOptions)

	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "Metrics bind address. Use 0 to disable.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe bind address.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election.")
	flag.StringVar(&watchNamespace, "namespace", "", "Namespace to watch. Empty means all namespaces.")
	flag.StringVar(&watchFilter, "watch-filter", "", "Cluster API watch filter label value.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true, "Serve metrics over HTTPS.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "Webhook certificate directory.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "Webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "Webhook private key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "", "Metrics certificate directory.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "Metrics certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "Metrics private key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false, "Enable HTTP/2 for metrics and webhooks.")

	goFlagSet := flag.CommandLine
	pflag.CommandLine.AddGoFlagSet(goFlagSet)
	pflag.Parse()

	ctrl.SetLogger(klog.Background())

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) {
			c.NextProtos = []string{"http/1.1"}
		})
	}

	webhookServerOptions := webhook.Options{TLSOpts: tlsOpts}
	if webhookCertPath != "" {
		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if metricsCertPath != "" {
		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	options := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhook.NewServer(webhookServerOptions),
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "cluster-api-ipam-provider-netbox.ipam.cluster.x-k8s.io",
	}
	if watchNamespace != "" {
		options.Cache = cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				watchNamespace: {},
			},
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	if err := index.SetupIndexes(ctx, mgr); err != nil {
		setupLog.Error(err, "Failed to set up indexes")
		os.Exit(1)
	}

	if err := (&ipamutil.ClaimReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		WatchFilterValue: watchFilter,
		Adapter:          &controller.NetBoxProviderAdapter{},
	}).SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "Failed to create IPAddressClaim reconciler")
		os.Exit(1)
	}

	if err := (&controller.NetBoxIPPoolReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create NetBoxIPPool reconciler")
		os.Exit(1)
	}

	if err := (&controller.GlobalNetBoxIPPoolReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create GlobalNetBoxIPPool reconciler")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager", "version", version.Get().String())
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
