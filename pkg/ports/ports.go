// Package ports defines the canonical interface contracts (ports in the
// Ports & Adapters / Hexagonal Architecture) between the pipeline orchestrator
// and each domain implementation.
//
// Rules enforced by this file:
//   - Interfaces are defined here; implementations live in internal/.
//   - The pipeline orchestrator depends ONLY on these interfaces, never on
//     concrete types from internal/.
//   - Concrete adapters are injected at startup in cmd/deploy.go.
//
// This enables complete unit-testability: swap any adapter for a mock without
// touching the pipeline or any other domain.
package ports

import (
	"context"
	"io"

	"github.com/francisco3ferraz/vessel-cli/internal/pipeline"
)

// ─── PORT 0a: Workspace Initializer ──────────────────────────────────────────
// Manages the .vessel-cli/ directory lifecycle and the exclusive file lock
// that prevents concurrent deployments from corrupting terraform.tfstate.

type WorkspaceInitializer interface {
	// Init creates the workspace directory structure and injects the
	// .gitignore entry. Idempotent: safe to call on every run.
	Init() error

	// AcquireLock acquires an exclusive advisory lock on .vessel-cli/.lock.
	// Returns immediately with an error (non-blocking) if another process
	// holds the lock. The caller MUST call ReleaseLock when done.
	AcquireLock() error

	// ReleaseLock releases the workspace lock. Safe to call if no lock is held.
	ReleaseLock()
}

// ─── PORT 0b: Preflight Checker ──────────────────────────────────────────────
// Runs all pre-deployment environment checks before any cloud or Docker
// resource is created. Collects ALL failures before returning so the user
// sees every problem in one pass rather than fix-and-retry cycling.

type PreflightChecker interface {
	// Check runs all six preflight checks and returns an aggregate error
	// if any fail. On success, populates pctx.AWSAccountID and pctx.CallerIP.
	Check(ctx context.Context, pctx *pipeline.PipelineContext) error
}

// ─── PORT 1: Workspace Inspector ─────────────────────────────────────────────
// Validates a directory is a Go project and extracts its metadata.
// Failure here is a hard stop — we cannot proceed without a valid project.

type WorkspaceInspector interface {
	// Inspect scans pctx.ProjectDir, validates go.mod + main package,
	// parses Go version and module name, and checks git state.
	// Sets pctx.GoVersion, pctx.ModuleName, pctx.BinaryName, pctx.ImageTag.
	// Returns an error if the tree is dirty and pctx.TagOverride is not set.
	Inspect(ctx context.Context, pctx *pipeline.PipelineContext) error
}

// ─── PORT 2: Artifact Generator ──────────────────────────────────────────────
// Generates all static file artifacts (Dockerfile) into the workspace.
// Pure function: same inputs always produce the same outputs. Idempotent.

type ArtifactGenerator interface {
	// GenerateDockerfile renders the multi-stage Dockerfile template using
	// metadata from PipelineContext and writes it to the project directory.
	// Also writes .dockerignore if not already present (never overwrites).
	// Sets pctx.DockerfilePath on success.
	GenerateDockerfile(ctx context.Context, pctx *pipeline.PipelineContext) error
}

// ─── PORT 3: Docker Compiler ─────────────────────────────────────────────────
// Owns the entire Docker image build lifecycle via the Docker SDK.
// Streams build output as structured events for real-time UI rendering.

type DockerCompiler interface {
	// Build orchestrates: tag generation → docker build → enriched context.
	// Streams structured BuildEvents to the provided channel.
	// Sets pctx.ImageTag and pctx.ImageID on success.
	Build(ctx context.Context, pctx *pipeline.PipelineContext, events chan<- BuildEvent) error

	// Push authenticates with ECR (via ecr:GetAuthorizationToken) and pushes
	// the built image. Skips push if the tag already exists (ecr:DescribeImages).
	// Reads pctx.ImageTag and pctx.CloudOutputs.ECRRepositoryURI.
	Push(ctx context.Context, pctx *pipeline.PipelineContext, events chan<- BuildEvent) error
}

// BuildEvent is a structured event from the Docker build/push stream.
// The UI renderer consumes these; domain logic never touches raw JSON.
type BuildEvent struct {
	Type    BuildEventType
	Message string
	Error   error  // non-nil only for BuildEventError
	Layer   string // Docker layer ID, if applicable
}

// BuildEventType enumerates Docker streaming event variants.
type BuildEventType int

const (
	BuildEventLog   BuildEventType = iota // General log line
	BuildEventStep                        // "Step N/M : ..."
	BuildEventPush                        // Push layer progress
	BuildEventError                       // Build or push error
	BuildEventDone                        // Build or push complete
)

// ─── PORT 4: IaC Renderer ────────────────────────────────────────────────────
// Renders Terraform templates into the managed .vessel-cli/tf/ workspace.
// Pure rendering concern; does NOT invoke terraform.
//
// Backend-agnostic: BackendConfig controls which Terraform backend block is
// rendered. v1 ships LocalBackend only. Adding S3BackendConfig in v2 requires
// zero changes to this interface.

type IaCRenderer interface {
	// Render generates main.tf, variables.tf, outputs.tf, and terraform.tfvars
	// into the workspace using data from PipelineContext and BackendConfig.
	// Idempotent: safe to call on every run; Terraform state handles drift.
	// Sets pctx.TFWorkDir on success.
	Render(ctx context.Context, pctx *pipeline.PipelineContext, backend BackendConfig) error
}

// BackendConfig is a discriminated union for the Terraform backend block.
// The renderer switches on BackendType to emit the correct HCL stanza.
// v1 implements BackendTypeLocal only.
type BackendConfig struct {
	Type BackendType

	// Local (v1): no additional fields — tfstate goes to workDir automatically.

	// S3 (v2, not yet implemented):
	S3Bucket      string // e.g., "my-org-tfstate"
	S3Key         string // e.g., "vessel-cli/my-service/terraform.tfstate"
	S3Region      string
	DynamoDBTable string // for state locking
}

// BackendType distinguishes Terraform backend implementations.
type BackendType string

const (
	BackendTypeLocal BackendType = "local" // v1: tfstate in .vessel-cli/tf/
	BackendTypeS3    BackendType = "s3"    // v2: renderer returns ErrNotImplemented
)

// ─── PORT 5: Terraform Executor ──────────────────────────────────────────────
// Wraps the terraform binary to provide a typed, streamable IaC execution API.

type TerraformExecutor interface {
	// Init runs `terraform init` in the workspace, downloading providers.
	// Idempotent: safe to call on every deploy.
	Init(ctx context.Context, workDir string, logWriter io.Writer) error

	// Apply runs `terraform apply -auto-approve`, streams logs to logWriter,
	// then parses JSON outputs and populates pctx.CloudOutputs.
	Apply(ctx context.Context, workDir string, pctx *pipeline.PipelineContext, logWriter io.Writer) error

	// Output parses `terraform output -json` and returns typed CloudOutputs.
	// Used to recover state on subsequent runs without re-applying.
	Output(ctx context.Context, workDir string) (*pipeline.CloudOutputs, error)
}

// ─── PORT 6: State Manager ───────────────────────────────────────────────────
// Persists and recovers inter-run state from .vessel-cli/state.json.
// Decoupled from WorkspaceInitializer for single-responsibility.

type StateManager interface {
	// Load reads the persisted DeploymentState for the given projectDir.
	// Returns a zero-value state (not an error) if no state file exists yet.
	Load(projectDir string) (*DeploymentState, error)

	// Save atomically writes state after a successful pipeline run.
	// Uses write-to-tmp + os.Rename to prevent state corruption on failure.
	// Only called on full pipeline success — a failed stage does not
	// overwrite the previous valid state.json.
	Save(projectDir string, state *DeploymentState) error
}

// DeploymentState is the schema for .vessel-cli/state.json.
// This is vessel-cli's own bookkeeping — NOT the Terraform state.
type DeploymentState struct {
	AppName          string `json:"app_name"`
	AWSRegion        string `json:"aws_region"`
	LastImageTag     string `json:"last_image_tag"`
	ECRRepositoryURI string `json:"ecr_repository_uri"`
	ECSClusterARN    string `json:"ecs_cluster_arn"`
	ECSServiceARN    string `json:"ecs_service_arn"`
	// CachedCIDR is set on first deploy and reused on all subsequent deploys
	// so the SG rule is stable across IP-rotating networks (Q9).
	CachedCIDR     string `json:"cached_cidr"`
	LastDeployedAt string `json:"last_deployed_at"` // RFC3339
}
