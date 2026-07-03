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

package netbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	ipamv1alpha1 "github.com/evenh/cluster-api-ipam-provider-netbox/api/v1alpha1"
)

const (
	SecretKeyURL                = "url"
	SecretKeyToken              = "token"
	SecretKeyInsecureSkipVerify = "insecureSkipVerify"
	SecretKeyCABundle           = "caBundle"
	httpClientTimeout           = 30 * time.Second
)

type ConnectionConfig struct {
	BaseURL            string
	Token              string
	InsecureSkipVerify bool
	CABundle           []byte
}

func LoadConnectionConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	ref ipamv1alpha1.NamespacedSecretReference,
) (ConnectionConfig, error) {
	secretNamespace := ref.Namespace
	if secretNamespace == "" {
		secretNamespace = namespace
	}

	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: secretNamespace, Name: ref.Name}, secret); err != nil {
		return ConnectionConfig{}, fmt.Errorf("get connection secret: %w", err)
	}

	cfg := ConnectionConfig{
		BaseURL: string(secret.Data[SecretKeyURL]),
		Token:   string(secret.Data[SecretKeyToken]),
	}
	if cfg.BaseURL == "" {
		return ConnectionConfig{}, fmt.Errorf(
			"connection secret %s/%s is missing %q",
			secretNamespace,
			ref.Name,
			SecretKeyURL,
		)
	}
	if cfg.Token == "" {
		return ConnectionConfig{}, fmt.Errorf(
			"connection secret %s/%s is missing %q",
			secretNamespace,
			ref.Name,
			SecretKeyToken,
		)
	}

	if raw, ok := secret.Data[SecretKeyInsecureSkipVerify]; ok && len(raw) > 0 {
		value, err := strconv.ParseBool(string(raw))
		if err != nil {
			return ConnectionConfig{}, fmt.Errorf("parse %q: %w", SecretKeyInsecureSkipVerify, err)
		}
		cfg.InsecureSkipVerify = value
	}
	cfg.CABundle = secret.Data[SecretKeyCABundle]
	return cfg, nil
}

func NewHTTPClient(cfg ConnectionConfig) (*http.Client, error) {
	transport := &http.Transport{
		// Proxy honors HTTP_PROXY, HTTPS_PROXY, and NO_PROXY (and their lowercase forms) from the
		// manager process environment. Unlike http.DefaultTransport, a Transport literal defaults
		// Proxy to nil, so this must be set explicitly or proxy env vars are silently ignored.
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			//nolint:gosec // The secret explicitly controls whether certificate verification is skipped.
			InsecureSkipVerify: cfg.InsecureSkipVerify,
		},
	}
	if len(cfg.CABundle) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CABundle) {
			return nil, errors.New("parse CA bundle")
		}
		transport.TLSClientConfig.RootCAs = pool
	}

	return &http.Client{
		Transport: transport,
		Timeout:   httpClientTimeout,
	}, nil
}

func SplitBaseURL(rawURL string) (string, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("url %q must include scheme and host", rawURL)
	}
	return parsed.Scheme, parsed.Host, nil
}
