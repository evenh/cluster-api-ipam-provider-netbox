//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestE2E provisions a throwaway NetBox + kind environment and runs the chainsaw
// scenarios against it, unless E2E_REUSE is set, in which case it runs against
// the environment already provisioned by `make e2e-up` (see test/e2e/cmd/e2eup)
// without provisioning or tearing anything down. This lets you keep a cluster
// running and iterate on manager code or chainsaw assertions without paying for
// NetBox/kind startup on every run.
//
//nolint:tparallel // scenarios share one kind cluster, NetBox instance, and manager process; run sequentially.
func TestE2E(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping e2e in short mode")
	}

	projectDir, setupErr := os.Getwd()
	if setupErr != nil {
		t.Fatalf("getwd: %v", setupErr)
	}
	projectDir, setupErr = ResolveProjectDir(projectDir)
	if setupErr != nil {
		t.Fatalf("resolve project dir: %v", setupErr)
	}

	ctx := t.Context()

	var env *Environment
	var namespacedPrefixID int32
	var globalPrefixCIDR string

	if os.Getenv("E2E_REUSE") != "" {
		env, namespacedPrefixID, globalPrefixCIDR = reuseEnvironment(ctx, t, projectDir)
	} else {
		env, namespacedPrefixID, globalPrefixCIDR = provisionEnvironment(ctx, t, projectDir)
		defer env.Cleanup()
	}

	scenarios := []scenario{
		{
			name:            "namespaced-pool-by-prefix-id",
			dir:             filepath.Join(chainsawScenarioRoot, "namespaced-ipv4"),
			namespace:       "e2e-namespaced",
			poolName:        "netbox-pool",
			claimName:       "claim",
			expectedDNSName: "claimed.example.com",
			expectedStatus:  "active",
			expectedTags: []string{
				defaultOwnershipTag,
				"claim-override",
			},
			expectedCustomFields: map[string]string{
				"source": "chainsaw",
				"owner":  "namespaced",
			},
			values: map[string]string{
				"namespace":   "e2e-namespaced",
				"netboxURL":   env.NetBoxURL(),
				"netboxToken": env.NetBoxToken(),
				"prefixID":    strconv.Itoa(int(namespacedPrefixID)),
			},
		},
		{
			name:            "global-pool-by-cidr",
			dir:             filepath.Join(chainsawScenarioRoot, "global-ipv4"),
			namespace:       "e2e-global",
			poolName:        "global-netbox-pool",
			claimName:       "claim",
			expectedDNSName: "global.example.com",
			expectedStatus:  "reserved",
			expectedTags: []string{
				defaultOwnershipTag,
				"global-default",
			},
			expectedCustomFields: map[string]string{
				"source": "global",
			},
			values: map[string]string{
				"namespace":   "e2e-global",
				"netboxURL":   env.NetBoxURL(),
				"netboxToken": env.NetBoxToken(),
				"prefixCIDR":  globalPrefixCIDR,
			},
			extraCleanupResources: []cleanupResource{{
				kind: "globalnetboxippool.ipam.cluster.x-k8s.io",
				name: "global-netbox-pool",
			}},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			runScenario(ctx, t, env, scenario)
		})
	}
}

// reuseEnvironment wraps the environment already provisioned by `make e2e-up`
// (see test/e2e/cmd/e2eup), skipping NetBox/kind provisioning entirely.
func reuseEnvironment(ctx context.Context, t *testing.T, projectDir string) (*Environment, int32, string) {
	t.Helper()

	state, err := LoadEnvironmentState(projectDir)
	if err != nil {
		t.Fatalf("%v", err)
	}
	env, err := EnvironmentFromState(ctx, t.TempDir(), state)
	if err != nil {
		t.Fatalf("wrap reused e2e environment: %v", err)
	}
	return env, state.NamespacedPrefixID, state.GlobalPrefixCIDR
}

// provisionEnvironment provisions a throwaway NetBox + kind environment for the
// duration of this test run. The caller is responsible for deferring Cleanup.
func provisionEnvironment(ctx context.Context, t *testing.T, projectDir string) (*Environment, int32, string) {
	t.Helper()

	env := NewEnvironment(ctx, t.TempDir(), projectDir)
	if err := env.Setup(); err != nil {
		env.Cleanup()
		t.Fatalf("setup e2e environment: %v", err)
	}

	namespacedPrefix, err := env.CreatePrefix(ctx, "10.203.1.0/24")
	if err != nil {
		env.Cleanup()
		t.Fatalf("create namespaced prefix: %v", err)
	}
	globalPrefix, err := env.CreatePrefix(ctx, "10.203.2.0/24")
	if err != nil {
		env.Cleanup()
		t.Fatalf("create global prefix: %v", err)
	}
	return env, namespacedPrefix.ID, globalPrefix.Prefix
}

func runScenario(ctx context.Context, t *testing.T, env *Environment, scenario scenario) {
	t.Helper()

	err := env.RunChainsawScenario(ctx, scenario.dir, scenario.values)
	if err != nil {
		t.Fatalf("run chainsaw scenario: %v", err)
	}

	claimUID, err := env.GetClaimUID(ctx, scenario.namespace, scenario.claimName)
	if err != nil {
		t.Fatalf("get claim uid: %v", err)
	}

	ipAddress, err := env.FindIPAddressByClaimUID(ctx, defaultOwnershipTag, defaultClaimUIDField, claimUID)
	if err != nil {
		t.Fatalf("find ip by claim uid: %v", err)
	}
	if ipAddress == nil {
		t.Fatal("expected NetBox IP to exist after scenario")
	}

	env.AssertIPAddress(t, ipAddress, scenario)

	err = env.DeleteClaim(ctx, scenario.namespace, scenario.claimName)
	if err != nil {
		t.Fatalf("delete claim: %v\n%s", err, env.failureDetails(ctx))
	}
	err = env.WaitForIPAddressDeleted(
		ctx,
		defaultOwnershipTag,
		defaultClaimUIDField,
		claimUID,
		resourceCleanupTimeout,
	)
	if err != nil {
		t.Fatalf("wait for NetBox IP deletion: %v\n%s", err, env.failureDetails(ctx))
	}
	err = env.waitForResourceDeleted(
		ctx,
		scenario.namespace,
		"ipaddressclaims.ipam.cluster.x-k8s.io",
		scenario.claimName,
		resourceCleanupTimeout,
	)
	if err != nil {
		t.Fatalf("wait for claim deletion: %v\n%s", err, env.failureDetails(ctx))
	}
	err = env.waitForResourceDeleted(
		ctx,
		scenario.namespace,
		"ipaddresses.ipam.cluster.x-k8s.io",
		scenario.claimName,
		resourceCleanupTimeout,
	)
	if err != nil {
		t.Fatalf("wait for address deletion: %v\n%s", err, env.failureDetails(ctx))
	}
	err = env.CleanupScenario(ctx, scenario)
	if err != nil {
		t.Fatalf("cleanup scenario: %v\n%s", err, env.failureDetails(ctx))
	}
}

func (e *Environment) AssertIPAddress(t *testing.T, ipAddress *NetBoxIPAddress, scenario scenario) {
	t.Helper()

	if ipAddress.DNSName != scenario.expectedDNSName {
		t.Fatalf("unexpected dns name: got %q want %q", ipAddress.DNSName, scenario.expectedDNSName)
	}
	if ipAddress.Status == nil || ipAddress.Status.Value != scenario.expectedStatus {
		t.Fatalf("unexpected status: %#v", ipAddress.Status)
	}

	tags := make(map[string]struct{}, len(ipAddress.Tags))
	for _, tag := range ipAddress.Tags {
		tags[tag.Name] = struct{}{}
	}
	for _, expectedTag := range scenario.expectedTags {
		if _, ok := tags[expectedTag]; !ok {
			t.Fatalf("expected tag %q in %#v", expectedTag, ipAddress.Tags)
		}
	}

	for key, expectedValue := range scenario.expectedCustomFields {
		if got := fmt.Sprint(ipAddress.CustomFields[key]); got != expectedValue {
			t.Fatalf("unexpected custom field %q: got %q want %q", key, got, expectedValue)
		}
	}
}
