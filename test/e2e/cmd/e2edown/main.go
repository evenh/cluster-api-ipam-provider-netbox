// Command e2edown tears down the e2e environment provisioned by e2eup (see
// `make e2e-up` / `make e2e-down`).
//
//go:build e2e

package main

import (
	"errors"
	"fmt"
	"os"

	e2e "github.com/evenh/cluster-api-ipam-provider-netbox/test/e2e"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2edown:", err)
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

	state, err := e2e.LoadEnvironmentState(projectDir)
	if err != nil {
		fmt.Println("no e2e environment state found, nothing to tear down")
		//nolint:nilerr // missing state means nothing was ever provisioned; that's success, not failure.
		return nil
	}

	fmt.Println("tearing down e2e environment...")
	var teardownErrs []error
	teardownErrs = append(teardownErrs, e2e.Teardown(state)...)

	err = e2e.RemoveState(projectDir)
	if err != nil {
		teardownErrs = append(teardownErrs, fmt.Errorf("remove state file: %w", err))
	}
	err = os.RemoveAll(state.WorkDir)
	if err != nil {
		teardownErrs = append(teardownErrs, fmt.Errorf("remove work dir: %w", err))
	}

	if len(teardownErrs) > 0 {
		return errors.Join(teardownErrs...)
	}

	fmt.Println("e2e environment torn down.")
	return nil
}
