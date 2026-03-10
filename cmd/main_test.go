package main

import (
	"flag"
	"testing"
)

func TestParseFlagsWithGoFlagSetAppliesOverrides(t *testing.T) {
	t.Parallel()

	goFlagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg, err := parseFlagsWithGoFlagSet([]string{
		"--metrics-bind-address=:9090",
		"--health-probe-bind-address=0",
		"--leader-elect=true",
		"--namespace=tenant-a",
		"--watch-filter=test-cluster",
		"--webhook-cert-path=/tmp/certs",
		"--webhook-cert-name=serving.crt",
		"--webhook-cert-key=serving.key",
		"--enable-http2=true",
	}, goFlagSet)
	if err != nil {
		t.Fatalf("parseFlagsWithGoFlagSet() error = %v", err)
	}

	if cfg.metricsAddr != ":9090" {
		t.Fatalf("metricsAddr = %q, want %q", cfg.metricsAddr, ":9090")
	}
	if cfg.probeAddr != "0" {
		t.Fatalf("probeAddr = %q, want %q", cfg.probeAddr, "0")
	}
	if !cfg.enableLeaderElection {
		t.Fatal("enableLeaderElection = false, want true")
	}
	if cfg.watchNamespace != "tenant-a" {
		t.Fatalf("watchNamespace = %q, want %q", cfg.watchNamespace, "tenant-a")
	}
	if cfg.watchFilter != "test-cluster" {
		t.Fatalf("watchFilter = %q, want %q", cfg.watchFilter, "test-cluster")
	}
	if cfg.webhookCertPath != "/tmp/certs" {
		t.Fatalf("webhookCertPath = %q, want %q", cfg.webhookCertPath, "/tmp/certs")
	}
	if cfg.webhookCertName != "serving.crt" {
		t.Fatalf("webhookCertName = %q, want %q", cfg.webhookCertName, "serving.crt")
	}
	if cfg.webhookCertKey != "serving.key" {
		t.Fatalf("webhookCertKey = %q, want %q", cfg.webhookCertKey, "serving.key")
	}
	if !cfg.enableHTTP2 {
		t.Fatal("enableHTTP2 = false, want true")
	}
}
