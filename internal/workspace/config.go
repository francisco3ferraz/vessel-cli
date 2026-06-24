package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// ProjectConfig represents the vessel.json file at the project root.
// This file is committed to Git so the whole team shares the same configuration.
type ProjectConfig struct {
	RemoteState        *types.RemoteStateConfig `json:"remote_state,omitempty"`
	DefaultEnvironment string                   `json:"default_environment,omitempty"` // e.g. "staging"; overridden by --environment flag
}

// LoadProjectConfig reads vessel.json from the project root.
func LoadProjectConfig(projectDir string) (*ProjectConfig, error) {
	path := filepath.Join(projectDir, "vessel.json")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ProjectConfig{}, nil
		}
		return nil, fmt.Errorf("open vessel.json: %w", err)
	}
	defer f.Close()

	var cfg ProjectConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode vessel.json: %w", err)
	}
	return &cfg, nil
}

// SaveProjectConfig writes vessel.json to the project root.
func SaveProjectConfig(projectDir string, cfg *ProjectConfig) error {
	path := filepath.Join(projectDir, "vessel.json")
	
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create vessel.json: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode vessel.json: %w", err)
	}
	return nil
}
