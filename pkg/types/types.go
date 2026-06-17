// Package types defines the shared data structures used across the
// vessel-cli pipeline. It is a deliberate leaf package — it has zero
// internal imports — so any package in the project may import it
// without risk of creating circular dependencies.
package types

// ─── Pipeline envelope ────────────────────────────────────────────────────────

// PipelineContext is the immutable-intent, mutable-state envelope that each
// pipeline stage reads from and writes to. It is the contract between stages
// and eliminates global state entirely.
//
// Fields are populated progressively as the pipeline advances through its stages.
// A stage MAY read any field set by a prior stage; it MUST NOT read fields
// from future stages (enforced by ordering in Orchestrator.Run).
type PipelineContext struct {
	// ── Stage 0: CLI inputs (set once by cmd/deploy.go) ──────────────────────
	ProjectDir string // Absolute path to the target Go project
	AWSRegion  string // e.g., "us-east-1"
	AWSProfile string // e.g., "default"
	AppName    string // From --name flag; overrides BinaryName if set

	// ── Stage 0: Behavior flags ───────────────────────────────────────────────
	TagOverride string // --tag: bypass git SHA and dirty-tree check (Q3)
	AllowPublic bool   // --allow-public: use 0.0.0.0/0 for SG ingress (Q8)
	DryRun      bool   // --dry-run: plan only, no apply
	Destroy     bool   // --destroy: remove all cloud resources
	SkipConfirm bool   // --yes: skip plan-and-confirm prompt (for CI)

	// ── Stage 0: Preflight outputs ────────────────────────────────────────────
	AWSAccountID string // From sts:GetCallerIdentity
	CallerIP     string // Detected public IP /32, or cached from state.json (Q9)

	// ── Stage 1: Workspace inspection outputs ────────────────────────────────
	GoVersion     string // Parsed from go.mod, e.g., "1.22"
	ModuleName    string // e.g., "github.com/acme/my-service"
	BinaryName    string // Last path segment of ModuleName
	IsFirstDeploy bool   // true when no .vessel-cli/state.json exists yet

	// ── Stage 2: Artifact generation outputs ─────────────────────────────────
	DockerfilePath string // Absolute path to the generated Dockerfile

	// ── Stage 3: Docker build outputs ────────────────────────────────────────
	ImageTag string // e.g., "my-service:a3f5b9c1"
	ImageID  string // Full Docker image ID

	// ── Stage 4: IaC render outputs ──────────────────────────────────────────
	TFWorkDir string // Absolute path to .vessel-cli/tf/

	// ── Stage 5: Terraform apply outputs ─────────────────────────────────────
	CloudOutputs CloudOutputs
}

// CloudOutputs captures the Terraform outputs after a successful apply.
type CloudOutputs struct {
	ECRRepositoryURI string // e.g., "123456789.dkr.ecr.us-east-1.amazonaws.com/my-service"
	ECSClusterARN    string
	ECSServiceARN    string
	ECSTaskDefARN    string
}

// ─── Persistence schema ───────────────────────────────────────────────────────

// DeploymentState is the schema for .vessel-cli/state.json.
// This is vessel-cli's own bookkeeping — NOT the Terraform state file.
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
