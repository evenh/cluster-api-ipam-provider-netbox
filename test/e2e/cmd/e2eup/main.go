// Command e2eup provisions a long-lived NetBox + kind e2e environment that a
// human can point tests, chainsaw, or kubectl at repeatedly instead of paying
// for a fresh NetBox/kind bring-up on every `go test` run. Tear it down with
// e2edown (see `make e2e-down`).
//
//go:build e2e

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	e2e "github.com/evenh/cluster-api-ipam-provider-netbox/test/e2e"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2eup:", err)
		os.Exit(1)
	}
}

func run() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	projectDir, err := e2e.ResolveProjectDir(cwd)
	if err != nil {
		return err
	}

	// Containers must survive this process exiting, so opt the ryuk reaper out
	// before touching docker at all.
	err = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	if err != nil {
		return fmt.Errorf("disable ryuk: %w", err)
	}

	previous, err := e2e.LoadEnvironmentState(projectDir)
	if err == nil {
		fmt.Println("found a previous e2e environment, tearing it down first...")
		for _, tErr := range e2e.Teardown(previous) {
			fmt.Fprintln(os.Stderr, "warning:", tErr)
		}
	}

	workDir := filepath.Join(projectDir, ".cache", "e2e")
	err = os.MkdirAll(workDir, 0o750)
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}

	ctx := context.Background()
	env := e2e.NewEnvironment(ctx, workDir, projectDir)

	fmt.Println("provisioning NetBox + kind e2e environment (this can take a few minutes)...")
	err = env.Setup()
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}

	namespacedPrefix, err := env.CreatePrefix(ctx, "10.203.1.0/24")
	if err != nil {
		return fmt.Errorf("create namespaced prefix: %w", err)
	}
	globalPrefix, err := env.CreatePrefix(ctx, "10.203.2.0/24")
	if err != nil {
		return fmt.Errorf("create global prefix: %w", err)
	}

	state := &e2e.EnvironmentState{
		ProjectDir:          projectDir,
		WorkDir:             workDir,
		KubeconfigPath:      env.KubeconfigPath,
		ClusterName:         env.ClusterName,
		NetBoxURL:           env.NetBoxURL(),
		NetBoxToken:         env.NetBoxToken(),
		ManagerPID:          env.ManagerPID(),
		ManagerLogPath:      env.ManagerLogPath(),
		DockerNetworkID:     env.DockerNetworkID(),
		PostgresContainerID: env.PostgresContainerID(),
		ValkeyContainerID:   env.ValkeyContainerID(),
		NetBoxContainerID:   env.NetBoxContainerID(),
		NamespacedPrefixID:  namespacedPrefix.ID,
		GlobalPrefixCIDR:    globalPrefix.Prefix,
	}
	err = state.Save()
	if err != nil {
		return fmt.Errorf("save environment state: %w", err)
	}

	printSummary(state)
	return nil
}

func printSummary(state *e2e.EnvironmentState) {
	contextName := e2e.KindContextName(state.ClusterName)

	fmt.Println()
	fmt.Println("e2e environment is up.")
	fmt.Println()
	fmt.Println("kubectl:")
	fmt.Printf("  export KUBECONFIG=%s\n", state.KubeconfigPath)
	fmt.Printf("  kubectl --context %s get ipaddressclaims,ipaddresses -A\n", contextName)
	fmt.Println()
	fmt.Println("manager:")
	fmt.Printf("  pid: %d\n", state.ManagerPID)
	fmt.Printf("  tail -f %s\n", state.ManagerLogPath)
	fmt.Println()
	fmt.Println("netbox:")
	fmt.Printf("  url:   %s (admin / admin)\n", state.NetBoxURL)
	fmt.Printf("  token: %s\n", state.NetBoxToken)
	fmt.Printf(
		"  seeded prefixes: id=%d (namespaced), cidr=%s (global)\n",
		state.NamespacedPrefixID,
		state.GlobalPrefixCIDR,
	)
	fmt.Println()
	fmt.Println("run tests against this environment:")
	fmt.Println("  make e2e-test-reuse")
	fmt.Println("  # or directly: E2E_REUSE=1 go test -tags=e2e ./test/e2e -run TestE2E -v")
	fmt.Println()
	fmt.Println("run chainsaw directly against a scenario:")
	fmt.Printf(
		"  go tool chainsaw test --config test/e2e/chainsaw.yaml --kube-context %s --skip-delete test/e2e/scenarios/<scenario>\n",
		contextName,
	)
	fmt.Println()
	fmt.Println("tear down:")
	fmt.Println("  make e2e-down")
}
