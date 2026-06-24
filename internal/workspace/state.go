package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// StateManager implements ports.StateManager using local state.json or S3.
// State is namespaced by environment: state.json (default), state.staging.json, etc.
type StateManager struct{}

func NewStateManager() *StateManager { return &StateManager{} }

// stateFilePath returns the local path for state, namespaced by environment.
// env="" → .vessel-cli/state.json
// env="staging" → .vessel-cli/state.staging.json
func stateFilePath(projectDir, env string) string {
	base := ".vessel-cli"
	filename := "state.json"
	if env != "" {
		filename = "state." + env + ".json"
	}
	return filepath.Join(projectDir, base, filename)
}

// s3StateKey returns the S3 object key for state, namespaced by appID and environment.
// env="" → <appID>/state.json
// env="staging" → <appID>/staging/state.json
func s3StateKey(projectDir string, remote *types.RemoteStateConfig, env string) string {
	appID := filepath.Base(projectDir)
	if remote != nil && remote.AppID != "" {
		appID = remote.AppID
	}
	if env != "" {
		return fmt.Sprintf("%s/%s/state.json", appID, env)
	}
	return fmt.Sprintf("%s/state.json", appID)
}

func (s *StateManager) Load(ctx context.Context, projectDir string, remote *types.RemoteStateConfig) (*types.DeploymentState, error) {
	// Extract environment from remote state config context (passed via convention
	// by callers who set RemoteState.AppID with env-prefixed naming).
	// For simplicity, we expose an environment field via the state key logic;
	// callers pass env through the RemoteState.AppID pattern or via helper below.
	return s.LoadForEnv(ctx, projectDir, "", remote)
}

// LoadForEnv loads state for the given environment name.
func (s *StateManager) LoadForEnv(ctx context.Context, projectDir, env string, remote *types.RemoteStateConfig) (*types.DeploymentState, error) {
	if remote != nil && remote.Bucket != "" {
		return s.loadRemote(ctx, projectDir, env, remote)
	}
	return s.loadLocal(projectDir, env)
}

func (s *StateManager) loadRemote(ctx context.Context, projectDir, env string, remote *types.RemoteStateConfig) (*types.DeploymentState, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(remote.Region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	key := s3StateKey(projectDir, remote, env)

	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(remote.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return &types.DeploymentState{}, nil
		}
		var nf *s3types.NotFound
		if errors.As(err, &nf) {
			return &types.DeploymentState{}, nil
		}
		return nil, fmt.Errorf("s3 get state.json: %w", err)
	}
	defer out.Body.Close()

	var state types.DeploymentState
	if err := json.NewDecoder(out.Body).Decode(&state); err != nil {
		return nil, fmt.Errorf("decode remote state.json: %w", err)
	}
	return &state, nil
}

func (s *StateManager) loadLocal(projectDir, env string) (*types.DeploymentState, error) {
	path := stateFilePath(projectDir, env)
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
		return nil, fmt.Errorf("corrupt local state.json: %w", err)
	}
	return &state, nil
}

func (s *StateManager) Save(ctx context.Context, projectDir string, remote *types.RemoteStateConfig, state *types.DeploymentState) error {
	return s.SaveForEnv(ctx, projectDir, state.Environment, remote, state)
}

// SaveForEnv saves state for the given environment name.
func (s *StateManager) SaveForEnv(ctx context.Context, projectDir, env string, remote *types.RemoteStateConfig, state *types.DeploymentState) error {
	state.LastDeployedAt = time.Now().UTC().Format(time.RFC3339)

	// Always save local first.
	if err := s.saveLocal(projectDir, env, state); err != nil {
		return err
	}

	if remote != nil && remote.Bucket != "" {
		return s.saveRemote(ctx, projectDir, env, remote, state)
	}
	return nil
}

func (s *StateManager) saveRemote(ctx context.Context, projectDir, env string, remote *types.RemoteStateConfig, state *types.DeploymentState) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(remote.Region))
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	key := s3StateKey(projectDir, remote, env)

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(remote.Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("s3 put state.json: %w", err)
	}
	return nil
}

func (s *StateManager) saveLocal(projectDir, env string, state *types.DeploymentState) error {
	path := stateFilePath(projectDir, env)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

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

func (s *StateManager) Delete(ctx context.Context, projectDir string, remote *types.RemoteStateConfig) error {
	return s.DeleteForEnv(ctx, projectDir, "", remote)
}

// DeleteForEnv removes state for the given environment name.
func (s *StateManager) DeleteForEnv(ctx context.Context, projectDir, env string, remote *types.RemoteStateConfig) error {
	// Always delete local.
	path := stateFilePath(projectDir, env)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove state.json: %w", err)
	}

	if remote != nil && remote.Bucket != "" {
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(remote.Region))
		if err != nil {
			return fmt.Errorf("aws config: %w", err)
		}
		client := s3.NewFromConfig(cfg)
		key := s3StateKey(projectDir, remote, env)
		_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(remote.Bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return fmt.Errorf("s3 delete state.json: %w", err)
		}
	}

	return nil
}
