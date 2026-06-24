// Package ports defines the canonical interface contracts (ports in the
// Ports & Adapters / Hexagonal Architecture) between the pipeline orchestrator
// and each domain adapter.
//
// Rules enforced by this file:
//   - Only interface definitions live here. No structs, no functions.
//   - Implementations live in internal/. Concrete types are injected at
//     startup in cmd/deploy.go.
//   - The only internal import allowed is pkg/types (the shared data leaf).
package ports

import (
	"context"
	"io"

	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// ─── PORT 0a: Workspace Initializer ──────────────────────────────────────────

type WorkspaceInitializer interface {
	// Init creates the workspace directory structure and injects the
	// .gitignore entry. Idempotent: safe to call on every run.
	Init() error

	// AcquireLock acquires an exclusive advisory lock on .vessel-cli/.lock.
	// Non-blocking: returns an error immediately if another process holds it.
	// The caller MUST call ReleaseLock when done.
	AcquireLock() error

	// ReleaseLock releases the workspace lock. Safe to call if no lock is held.
	ReleaseLock()
}

// ─── PORT 0b: Preflight Checker ──────────────────────────────────────────────

type PreflightChecker interface {
	// Check runs all six preflight checks, collects ALL failures before
	// returning (no fail-fast), and populates pctx.AWSAccountID and
	// pctx.CallerIP on success.
	Check(ctx context.Context, pctx *types.PipelineContext) error
}

// ─── PORT 1: Workspace Inspector ─────────────────────────────────────────────

type WorkspaceInspector interface {
	// Inspect validates the project directory, parses go.mod, checks git
	// state, and sets pctx.GoVersion, ModuleName, BinaryName, ImageTag.
	// Returns an error if the tree is dirty and pctx.TagOverride is not set.
	Inspect(ctx context.Context, pctx *types.PipelineContext) error
}

// ─── PORT 2: Artifact Generator ──────────────────────────────────────────────

type ArtifactGenerator interface {
	// GenerateDockerfile renders the multi-stage Dockerfile template and
	// writes .dockerignore if absent. Sets pctx.DockerfilePath on success.
	// Idempotent.
	GenerateDockerfile(ctx context.Context, pctx *types.PipelineContext) error
}

// ─── PORT 3: Docker Compiler ─────────────────────────────────────────────────

type DockerCompiler interface {
	// Build runs docker build, streams structured BuildEvents, and sets
	// pctx.ImageTag and pctx.ImageID on success.
	Build(ctx context.Context, pctx *types.PipelineContext, events chan<- BuildEvent) error

	// Push authenticates with ECR and pushes the image. Skips if the tag
	// already exists (ecr:DescribeImages). Reads pctx.ImageTag and
	// pctx.CloudOutputs.ECRRepositoryURI.
	Push(ctx context.Context, pctx *types.PipelineContext, events chan<- BuildEvent) error
}

// BuildEvent is a structured event from the Docker build/push stream.
type BuildEvent struct {
	Type    BuildEventType
	Message string
	Error   error  // non-nil only for BuildEventError
	Layer   string // Docker layer ID, if applicable
}

// BuildEventType enumerates Docker streaming event variants.
type BuildEventType int

const (
	BuildEventLog   BuildEventType = iota
	BuildEventStep
	BuildEventPush
	BuildEventError
	BuildEventDone
)

// ─── PORT 4: IaC Renderer ────────────────────────────────────────────────────

type IaCRenderer interface {
	// Render generates main.tf, variables.tf, outputs.tf, and
	// terraform.tfvars into the workspace. Sets pctx.TFWorkDir on success.
	// Idempotent.
	Render(ctx context.Context, pctx *types.PipelineContext, backend BackendConfig) error
}

// BackendConfig is a discriminated union for the Terraform backend block.
// v1 implements BackendTypeLocal only; v2 adds S3 with no interface change.
type BackendConfig struct {
	Type          BackendType
	S3Bucket      string // v2 only
	S3Key         string // v2 only
	S3Region      string // v2 only
	DynamoDBTable string // v2 only, for state locking
}

// BackendType distinguishes Terraform backend implementations.
type BackendType string

const (
	BackendTypeLocal BackendType = "local"
	BackendTypeS3    BackendType = "s3"
)

// ─── PORT 5: Terraform Executor ──────────────────────────────────────────────

type TerraformExecutor interface {
	// Init runs `terraform init`. Idempotent.
	Init(ctx context.Context, workDir string, logWriter io.Writer) error

	// Plan runs `terraform plan`, streams logs, and stops.
	Plan(ctx context.Context, workDir string, logWriter io.Writer) error

	// Apply runs `terraform apply -auto-approve`, streams logs, and
	// populates pctx.CloudOutputs.
	Apply(ctx context.Context, workDir string, pctx *types.PipelineContext, logWriter io.Writer) error

	// Destroy runs `terraform destroy -auto-approve` and streams logs.
	Destroy(ctx context.Context, workDir string, logWriter io.Writer) error

	// Output parses `terraform output -json` and returns typed CloudOutputs.
	Output(ctx context.Context, workDir string) (*types.CloudOutputs, error)
}

// ─── PORT 6: State Manager ───────────────────────────────────────────────────

type StateManager interface {
	// Load reads .vessel-cli/state.json. Returns a zero-value state
	// (not an error) if no file exists yet. If remote is configured,
	// loads from S3 instead.
	Load(ctx context.Context, projectDir string, remote *types.RemoteStateConfig) (*types.DeploymentState, error)

	// Save atomically writes state via tmp+rename. Only called on full
	// pipeline success. If remote is configured, pushes to S3.
	Save(ctx context.Context, projectDir string, remote *types.RemoteStateConfig, state *types.DeploymentState) error

	// Delete removes .vessel-cli/state.json. Called after a successful destroy.
	Delete(ctx context.Context, projectDir string, remote *types.RemoteStateConfig) error
}

// ─── PORT 7: ECS Deployer ─────────────────────────────────────────────────────

type Deployer interface {
	// Scale sets the ECS service desired count and blocks until the service
	// reaches a stable state (running == desired, no in-progress deployments)
	// or the context is cancelled.
	Scale(ctx context.Context, clusterARN, serviceARN string, desired int32) error
}

// ─── PORT 8: ECR Cleaner ──────────────────────────────────────────────────────

type ECRCleaner interface {
	// DeleteAllImages removes every image from the named ECR repository so that
	// `terraform destroy` can delete the repository without hitting
	// RepositoryNotEmptyException. Idempotent: succeeds if the repo is already
	// empty or does not exist.
	DeleteAllImages(ctx context.Context, region, repoName string) error
}

// ─── PORT 9: Secrets Manager ──────────────────────────────────────────────────

// SecretsManager manages sensitive deployment values in AWS Secrets Manager.
// Secret values written via --secret KEY=VALUE are stored here and injected
// into the ECS task definition via the `secrets` block (never in plaintext).
type SecretsManager interface {
	// PutSecret creates or updates a secret with the given name and value.
	// Returns the full ARN of the secret, which is used in the task definition.
	PutSecret(ctx context.Context, name, value string) (arn string, err error)

	// DeleteSecret permanently deletes a secret without a recovery window.
	// Safe to call on a non-existent secret (idempotent).
	DeleteSecret(ctx context.Context, name string) error
}

