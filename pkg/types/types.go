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
	ProjectDir  string // Absolute path to the target Go project
	AWSRegion   string // e.g., "us-east-1"
	AWSProfile  string // e.g., "default"
	AppName     string // From --name flag; overrides BinaryName if set
	Environment string // From --environment flag (e.g. "staging", "prod"). Empty = no namespace suffix.
	// EnvVars holds key=value pairs from --env flags, merged with any values
	// persisted in state.json from a previous deploy. CLI flags take precedence.
	EnvVars map[string]string // e.g., {"PORT": "8080"}
	// SecretVars holds key=value pairs from --secret flags. Values are written to
	// AWS Secrets Manager at deploy time and are NEVER persisted locally.
	SecretVars map[string]string // e.g., {"DB_PASSWORD": "hunter2"}
	// SecretARNs is populated by the Secrets sync stage (0.5). It maps each
	// secret key to its Secrets Manager ARN for injection into the task definition.
	SecretARNs map[string]string // populated at runtime, not persisted

	// ── Stage 0: Behavior flags ───────────────────────────────────────────────
	TagOverride string // --tag: bypass git SHA and dirty-tree check (Q3)
	AllowPublic bool   // --allow-public: use 0.0.0.0/0 for SG ingress (Q8)
	DryRun      bool   // --dry-run: plan only, no apply
	Destroy     bool   // --destroy: remove all cloud resources
	SkipConfirm bool   // --yes: skip plan-and-confirm prompt (for CI)

	// ── Stage 0: Resource sizing flags ───────────────────────────────────────
	CPU    int // Fargate task CPU units (256, 512, 1024, 2048, 4096)
	Memory int // Fargate task memory in MiB (512–30720)
	Port   int // Container port to expose (default 8080)

	// ── Stage 0: Networking flags ─────────────────────────────────────────────
	LoadBalancer   bool   // --load-balancer: provision an ALB in front of tasks
	CertificateARN string // --certificate-arn: ACM cert ARN; enables HTTPS listener

	// ── Stage 0: Remote State Configuration ──────────────────────────────────
	RemoteState *RemoteStateConfig // Populated if vessel.json exists or flags are passed

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
	TFWorkDir string // Absolute path to .vessel-cli/tf[-<env>]/

	// ── Stage 5: Terraform apply outputs ─────────────────────────────────────
	CloudOutputs CloudOutputs
}

// CloudOutputs captures the Terraform outputs after a successful apply.
type CloudOutputs struct {
	ECRRepositoryURI string // e.g., "123456789.dkr.ecr.us-east-1.amazonaws.com/my-service"
	ECSClusterARN    string
	ECSServiceARN    string
	ECSTaskDefARN    string
	ALBDNSName       string // e.g., "my-app-alb-1234.us-east-1.elb.amazonaws.com"
	ALBARN           string
}

// ─── Persistence schema ───────────────────────────────────────────────────────

// DeploymentState is the schema for .vessel-cli/state.json.
// This is vessel-cli's own bookkeeping — NOT the Terraform state file.
type DeploymentState struct {
	AppName          string            `json:"app_name"`
	Environment      string            `json:"environment,omitempty"`   // e.g. "staging", "prod"; empty = default
	AWSRegion        string            `json:"aws_region"`
	LastImageTag     string            `json:"last_image_tag"`
	ECRRepositoryURI string            `json:"ecr_repository_uri"`
	ECSClusterARN    string            `json:"ecs_cluster_arn"`
	ECSServiceARN    string            `json:"ecs_service_arn"`
	// EnvVars persists the environment variables across re-deploys.
	// A new --env flag overrides a persisted key; omitting --env preserves all
	// previously set vars. Use --env KEY= (empty value) to unset a key.
	EnvVars map[string]string `json:"env_vars,omitempty"`
	// SecretKeys lists the keys stored in AWS Secrets Manager for this deployment.
	// Secret VALUES are never persisted here — only the key names.
	// On re-deploy, existing keys are updated; keys absent from the new --secret
	// flags are preserved (use --secret KEY= to delete a secret).
	SecretKeys []string `json:"secret_keys,omitempty"`
	// CachedCIDR is set on first deploy and reused on all subsequent deploys
	// so the SG rule is stable across IP-rotating networks (Q9).
	CachedCIDR string `json:"cached_cidr"`
	// CPU, Memory, Port are persisted so re-deploys don't require re-passing flags.
	CPU    int `json:"cpu,omitempty"`    // Fargate task CPU units
	Memory int `json:"memory,omitempty"` // Fargate task memory in MiB
	Port   int `json:"port,omitempty"`   // Container port
	// LoadBalancer and CertificateARN persist ALB configuration across re-deploys.
	LoadBalancer   bool   `json:"load_balancer,omitempty"`
	CertificateARN string `json:"certificate_arn,omitempty"`
	ALBDNSName     string `json:"alb_dns_name,omitempty"`
	LastDeployedAt string `json:"last_deployed_at"` // RFC3339
}

// RemoteStateConfig is the schema for the project-level vessel.json file,
// which is committed to Git so the whole team uses the same state backend.
type RemoteStateConfig struct {
	Bucket string `json:"bucket"` // S3 bucket name
	Table  string `json:"table"`  // DynamoDB table name for locking
	Region string `json:"region"` // AWS Region of the S3 bucket
	AppID  string `json:"app_id"` // Used as the S3 prefix, defaults to directory basename
}
