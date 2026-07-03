//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// EnvironmentState is the persisted description of an environment provisioned by
// the e2eup tool, so a later `go test` invocation or the e2edown tool can find and
// act on it without holding any in-process handles.
type EnvironmentState struct {
	ProjectDir     string `json:"projectDir"`
	WorkDir        string `json:"workDir"`
	KubeconfigPath string `json:"kubeconfigPath"`
	ClusterName    string `json:"clusterName"`
	NetBoxURL      string `json:"netboxURL"`
	NetBoxToken    string `json:"netboxToken"`
	ManagerPID     int    `json:"managerPID"`
	ManagerLogPath string `json:"managerLogPath"`

	DockerNetworkID     string `json:"dockerNetworkID"`
	PostgresContainerID string `json:"postgresContainerID"`
	ValkeyContainerID   string `json:"valkeyContainerID"`
	NetBoxContainerID   string `json:"netboxContainerID"`

	NamespacedPrefixID int32  `json:"namespacedPrefixID"`
	GlobalPrefixCIDR   string `json:"globalPrefixCIDR"`
}

// StateFilePath returns the fixed location e2eup persists its state to, and where
// e2edown and `go test` (in reuse mode) read it back from.
func StateFilePath(projectDir string) string {
	return filepath.Join(projectDir, ".cache", "e2e", "state.json")
}

// Save writes the state to StateFilePath(s.ProjectDir), creating parent directories
// as needed.
func (s *EnvironmentState) Save() error {
	path := StateFilePath(s.ProjectDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal e2e environment state: %w", err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write e2e environment state: %w", err)
	}
	return nil
}

// LoadEnvironmentState reads back the state persisted by e2eup for projectDir.
func LoadEnvironmentState(projectDir string) (*EnvironmentState, error) {
	path := StateFilePath(projectDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf(
			"read e2e environment state at %s (run `make e2e-up` first): %w",
			path,
			err,
		)
	}
	var state EnvironmentState
	if err = json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse e2e environment state at %s: %w", path, err)
	}
	return &state, nil
}

// RemoveState deletes the persisted state file, if present. Missing files are not
// an error.
func RemoveState(projectDir string) error {
	err := os.Remove(StateFilePath(projectDir))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
