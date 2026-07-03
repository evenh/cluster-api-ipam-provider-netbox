package netbox

import (
	"context"
	"net/http"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
)

func TestLoadConnectionConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	t.Run("loads secret from pool namespace by default", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netbox",
				Namespace: "default",
			},
			Data: map[string][]byte{
				SecretKeyURL:                []byte("https://netbox.example.com"),
				SecretKeyToken:              []byte("token"),
				SecretKeyInsecureSkipVerify: []byte("true"),
			},
		}).Build()

		cfg, err := LoadConnectionConfig(
			context.Background(),
			client,
			"default",
			ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
		)
		if err != nil {
			t.Fatalf("LoadConnectionConfig() error = %v", err)
		}
		if cfg.BaseURL != "https://netbox.example.com" || cfg.Token != "token" || !cfg.InsecureSkipVerify {
			t.Fatalf("unexpected config: %#v", cfg)
		}
	})

	t.Run("loads secret from explicit namespace", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netbox",
				Namespace: "shared",
			},
			Data: map[string][]byte{
				SecretKeyURL:   []byte("https://netbox.example.com"),
				SecretKeyToken: []byte("token"),
			},
		}).Build()

		cfg, err := LoadConnectionConfig(
			context.Background(),
			client,
			"default",
			ipamv1alpha1.NamespacedSecretReference{
				Name:      "netbox",
				Namespace: "shared",
			},
		)
		if err != nil {
			t.Fatalf("LoadConnectionConfig() error = %v", err)
		}
		if cfg.BaseURL != "https://netbox.example.com" || cfg.Token != "token" {
			t.Fatalf("unexpected config: %#v", cfg)
		}
	})

	t.Run("returns error for invalid boolean", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netbox",
				Namespace: "default",
			},
			Data: map[string][]byte{
				SecretKeyURL:                []byte("https://netbox.example.com"),
				SecretKeyToken:              []byte("token"),
				SecretKeyInsecureSkipVerify: []byte("not-a-bool"),
			},
		}).Build()

		if _, err := LoadConnectionConfig(
			context.Background(),
			client,
			"default",
			ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
		); err == nil {
			t.Fatal("expected error for invalid insecureSkipVerify")
		}
	})

	t.Run("returns error when required keys are missing", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "netbox",
				Namespace: "default",
			},
			Data: map[string][]byte{
				SecretKeyURL: []byte("https://netbox.example.com"),
			},
		}).Build()

		if _, err := LoadConnectionConfig(
			context.Background(),
			client,
			"default",
			ipamv1alpha1.NamespacedSecretReference{Name: "netbox"},
		); err == nil {
			t.Fatal("expected error when token is missing")
		}
	})
}

func TestNewHTTPClient(t *testing.T) {
	t.Run("wires proxy support from the process environment", func(t *testing.T) {
		client, err := NewHTTPClient(ConnectionConfig{})
		if err != nil {
			t.Fatalf("NewHTTPClient() error = %v", err)
		}
		transport, ok := client.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("expected *http.Transport, got %T", client.Transport)
		}
		if transport.Proxy == nil {
			t.Fatal(
				"Proxy is nil: HTTP_PROXY/HTTPS_PROXY/NO_PROXY would be silently ignored " +
					"(a bare &http.Transport{} literal does not default Proxy the way http.DefaultTransport does)",
			)
		}
	})
}

func TestSplitBaseURL(t *testing.T) {
	t.Run("splits valid url", func(t *testing.T) {
		scheme, host, err := SplitBaseURL("https://netbox.example.com")
		if err != nil {
			t.Fatalf("SplitBaseURL() error = %v", err)
		}
		if scheme != "https" || host != "netbox.example.com" {
			t.Fatalf("unexpected split values: scheme=%q host=%q", scheme, host)
		}
	})

	t.Run("returns error for missing scheme", func(t *testing.T) {
		if _, _, err := SplitBaseURL("netbox.example.com"); err == nil {
			t.Fatal("expected error for url without scheme")
		}
	})
}
