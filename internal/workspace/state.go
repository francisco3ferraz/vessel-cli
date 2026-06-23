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
type StateManager struct{}

func NewStateManager() *StateManager { return &StateManager{} }

func getS3Key(projectDir string, remote *types.RemoteStateConfig) string {
	appID := filepath.Base(projectDir) // default
	if remote != nil && remote.AppID != "" {
		appID = remote.AppID
	}
	return fmt.Sprintf("%s/state.json", appID)
}

func (s *StateManager) Load(ctx context.Context, projectDir string, remote *types.RemoteStateConfig) (*types.DeploymentState, error) {
	if remote != nil && remote.Bucket != "" {
		return s.loadRemote(ctx, projectDir, remote)
	}
	return s.loadLocal(projectDir)
}

func (s *StateManager) loadRemote(ctx context.Context, projectDir string, remote *types.RemoteStateConfig) (*types.DeploymentState, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(remote.Region))
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	key := getS3Key(projectDir, remote)

	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(remote.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return &types.DeploymentState{}, nil
		}
		// Also check for NotFound which some S3 compatible APIs might return
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

func (s *StateManager) loadLocal(projectDir string) (*types.DeploymentState, error) {
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
		return nil, fmt.Errorf("corrupt local state.json: %w", err)
	}
	return &state, nil
}

func (s *StateManager) Save(ctx context.Context, projectDir string, remote *types.RemoteStateConfig, state *types.DeploymentState) error {
	state.LastDeployedAt = time.Now().UTC().Format(time.RFC3339)

	// Always save local first
	if err := s.saveLocal(projectDir, state); err != nil {
		return err
	}

	if remote != nil && remote.Bucket != "" {
		return s.saveRemote(ctx, projectDir, remote, state)
	}
	return nil
}

func (s *StateManager) saveRemote(ctx context.Context, projectDir string, remote *types.RemoteStateConfig, state *types.DeploymentState) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(remote.Region))
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg)
	key := getS3Key(projectDir, remote)

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

func (s *StateManager) saveLocal(projectDir string, state *types.DeploymentState) error {
	path := stateFilePath(projectDir)
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
	// Always delete local
	path := stateFilePath(projectDir)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove state.json: %w", err)
	}

	if remote != nil && remote.Bucket != "" {
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(remote.Region))
		if err != nil {
			return fmt.Errorf("aws config: %w", err)
		}
		client := s3.NewFromConfig(cfg)
		key := getS3Key(projectDir, remote)
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

func stateFilePath(projectDir string) string {
	return filepath.Join(projectDir, ".vessel-cli", "state.json")
}
