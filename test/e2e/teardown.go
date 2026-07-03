//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Teardown best-effort tears down everything described by state: the manager
// process, NetBox/postgres/valkey containers, the docker network, and the kind
// cluster. It collects and returns every failure instead of stopping at the first,
// since callers use this to clean up a possibly half-torn-down environment.
func Teardown(state *EnvironmentState) []error {
	var errs []error

	if state.ManagerPID > 0 {
		if proc, err := os.FindProcess(state.ManagerPID); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
			time.Sleep(managerShutdownGrace)
			_ = proc.Kill()
		}
	}

	for _, id := range []string{state.NetBoxContainerID, state.ValkeyContainerID, state.PostgresContainerID} {
		if id == "" {
			continue
		}
		if out, err := exec.Command("docker", "rm", "-f", id).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("remove container %s: %w\n%s", id, err, out))
		}
	}

	if state.DockerNetworkID != "" {
		if out, err := exec.Command("docker", "network", "rm", state.DockerNetworkID).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("remove network %s: %w\n%s", state.DockerNetworkID, err, out))
		}
	}

	if state.ClusterName != "" {
		if out, err := exec.Command("kind", "delete", "cluster", "--name", state.ClusterName).
			CombinedOutput(); err != nil {
			errs = append(errs, fmt.Errorf("delete kind cluster: %w\n%s", err, out))
		}
	}

	return errs
}
