// Package secrets implements the ports.SecretsManager interface using AWS Secrets Manager.
// Sensitive deployment values (passed via --secret KEY=VALUE) are stored here
// and injected into ECS task definitions via the `secrets` block.
// Values are NEVER written to state.json or Terraform state.
package secrets

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
)

// Manager implements ports.SecretsManager.
type Manager struct {
	client *secretsmanager.Client
	region string
	profile string
}

// compile-time interface guard
var _ ports.SecretsManager = &Manager{}

// NewManager creates a Manager configured for the given AWS region and profile.
func NewManager(region, profile string) *Manager {
	return &Manager{region: region, profile: profile}
}

func (m *Manager) client_(ctx context.Context) (*secretsmanager.Client, error) {
	if m.client != nil {
		return m.client, nil
	}
	opts := []func(*config.LoadOptions) error{config.WithRegion(m.region)}
	if m.profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(m.profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config for secrets manager: %w", err)
	}
	m.client = secretsmanager.NewFromConfig(cfg)
	return m.client, nil
}

// SecretName returns the standardised Secrets Manager secret name for a given
// appName and key. Format: /vessel/<appName>/<KEY>
// e.g. /vessel/myapp-staging/DB_PASSWORD
func SecretName(appName, key string) string {
	return fmt.Sprintf("/vessel/%s/%s", appName, key)
}

// PutSecret creates or updates a secret with the given name and value.
// Returns the full ARN of the secret for use in the ECS task definition.
func (m *Manager) PutSecret(ctx context.Context, name, value string) (string, error) {
	client, err := m.client_(ctx)
	if err != nil {
		return "", err
	}

	// Attempt to create first; if it already exists, update the value instead.
	createOut, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String(value),
		Description:  aws.String("Managed by vessel-cli. Do not edit manually."),
	})
	if err == nil {
		return aws.ToString(createOut.ARN), nil
	}

	// Secret already exists — update the value.
	var alreadyExists *smtypes.ResourceExistsException
	if !errors.As(err, &alreadyExists) {
		return "", fmt.Errorf("secretsmanager:CreateSecret %s: %w", name, err)
	}

	putOut, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretString: aws.String(value),
	})
	if err != nil {
		return "", fmt.Errorf("secretsmanager:PutSecretValue %s: %w", name, err)
	}
	return aws.ToString(putOut.ARN), nil
}

// DeleteSecret permanently deletes a secret without a recovery window.
// Idempotent: succeeds if the secret does not exist.
func (m *Manager) DeleteSecret(ctx context.Context, name string) error {
	client, err := m.client_(ctx)
	if err != nil {
		return err
	}

	t := true
	_, err = client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(name),
		ForceDeleteWithoutRecovery: &t,
	})
	if err != nil {
		var notFound *smtypes.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil // already gone — idempotent
		}
		return fmt.Errorf("secretsmanager:DeleteSecret %s: %w", name, err)
	}
	return nil
}
