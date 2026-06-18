package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// StateManager implements ports.StateManager using a local state.json file.
type StateManager struct{}

// NewStateManager returns a new StateManager.
func NewStateManager() *StateManager { return &StateManager{} }

// Load reads .vessel-cli/state.json for the given projectDir.
// Returns a zero-value DeploymentState (not an error) if no file exists.
func (s *StateManager) Load(projectDir string) (*types.DeploymentState, error) {
	path := stateFilePath(projectDir)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &types.DeploymentState{}, nil
		}
		return nil, fmt.Errorf("open state.json: %w", err)
	}
	defer f.Close()

	var state types.DeploymentState
	if err := json.NewDecoder(f).Decode(&state); err != nil {
		return nil, fmt.Errorf(
			"corrupt .vessel-cli/state.json: %w\n"+
				"  Fix: delete .vessel-cli/state.json and re-run deploy",
			err,
		)
	}
	return &state, nil
}

// Save atomically writes state to .vessel-cli/state.json.
// Uses write-to-tmp + os.Rename to prevent corruption on partial write.
// Only called on full pipeline success; a failed stage does NOT overwrite
// the previous valid state.json.
func (s *StateManager) Save(projectDir string, state *types.DeploymentState) error {
	state.LastDeployedAt = time.Now().UTC().Format(time.RFC3339)

	path := stateFilePath(projectDir)
	dir := filepath.Dir(path)

	// Write to a temp file in the same directory (same filesystem = Rename is atomic).
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if Rename succeeded

	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("flush state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	return nil
}

func stateFilePath(projectDir string) string {
	return filepath.Join(projectDir, ".vessel-cli", "state.json")
}

// Delete removes .vessel-cli/state.json for the given projectDir.
// Called after a successful `vessel-cli deploy --destroy`.
// Returns nil if the file does not exist (idempotent).
func (s *StateManager) Delete(projectDir string) error {
	path := stateFilePath(projectDir)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove state.json: %w", err)
	}
	return nil
}
