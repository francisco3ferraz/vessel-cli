package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	awsecr "github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/francisco3ferraz/vessel-cli/internal/artifact"
	"github.com/francisco3ferraz/vessel-cli/internal/docker"
	"github.com/francisco3ferraz/vessel-cli/internal/ecs"
	internalecr "github.com/francisco3ferraz/vessel-cli/internal/ecr"
	"github.com/francisco3ferraz/vessel-cli/internal/pipeline"
	"github.com/francisco3ferraz/vessel-cli/internal/terraform"
	"github.com/francisco3ferraz/vessel-cli/internal/ui"
	"github.com/francisco3ferraz/vessel-cli/internal/workspace"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy a Go microservice to AWS ECS Fargate",
	Long: `deploy inspects the current directory as a Go project, generates a
hardened Dockerfile, provisions infrastructure via Terraform, and deploys
your service to ECS Fargate in a single idempotent pipeline.

The command is safe to re-run: Terraform applies only the diff, the Docker push
is skipped if the image tag already exists in ECR, and ECS performs a rolling
replace only when the task definition changes.`,
	RunE: runDeploy,
}

func init() {
	rootCmd.AddCommand(deployCmd)
	deployCmd.Flags().String("name", "", "Application name (default: derived from go.mod module name)")
	deployCmd.Flags().String("tag", "", "Image tag override — bypasses git SHA check and dirty-tree gate (Q3)")
	deployCmd.Flags().Bool("yes", false, "Skip plan-and-confirm prompts (for CI)")
	deployCmd.Flags().Bool("allow-public", false, "Open port 8080 to 0.0.0.0/0 instead of caller's detected IP (Q6/Q8)")
	deployCmd.Flags().Bool("dry-run", false, "Render artifacts and show terraform plan; do not apply")
	deployCmd.Flags().Bool("destroy", false, "Remove all cloud resources for this app")
	deployCmd.Flags().StringArray("env", nil, "Environment variable in KEY=VALUE format (repeatable). Re-deploys preserve all set vars; use KEY= to unset.")
	deployCmd.Flags().Int("cpu", 0, "Fargate task CPU units (256, 512, 1024, 2048, 4096). Default 256. Persisted across re-deploys.")
	deployCmd.Flags().Int("memory", 0, "Fargate task memory in MiB (512-30720). Default 512. Persisted across re-deploys.")
	deployCmd.Flags().Int("port", 0, "Container port to expose (default 8080). Persisted across re-deploys.")
	deployCmd.Flags().Bool("load-balancer", false, "Provision an Application Load Balancer for a stable URL. Persisted across re-deploys.")
	deployCmd.Flags().String("certificate-arn", "", "ACM certificate ARN to enable HTTPS on the ALB. Requires --load-balancer.")
	deployCmd.Flags().String("state-bucket", "", "S3 bucket for remote state. Saves to vessel.json.")
	deployCmd.Flags().String("state-table", "", "DynamoDB table for remote state locking. Saves to vessel.json.")
	deployCmd.Flags().String("state-region", "", "AWS region for the remote state bucket. Defaults to the deploy region. Saves to vessel.json.")
}

func runDeploy(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()

	// ── Resolve flags ────────────────────────────────────────────────────────
	region, _ := cmd.Root().PersistentFlags().GetString("region")
	profile, _ := cmd.Root().PersistentFlags().GetString("profile")
	name, _ := cmd.Flags().GetString("name")
	tag, _ := cmd.Flags().GetString("tag")
	yes, _ := cmd.Flags().GetBool("yes")
	allowPublic, _ := cmd.Flags().GetBool("allow-public")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	destroy, _ := cmd.Flags().GetBool("destroy")
	envFlags, _ := cmd.Flags().GetStringArray("env")
	cpuFlag, _ := cmd.Flags().GetInt("cpu")
	memoryFlag, _ := cmd.Flags().GetInt("memory")
	portFlag, _ := cmd.Flags().GetInt("port")
	lbFlag, _ := cmd.Flags().GetBool("load-balancer")
	certARNFlag, _ := cmd.Flags().GetString("certificate-arn")

	stateBucket, _ := cmd.Flags().GetString("state-bucket")
	stateTable, _ := cmd.Flags().GetString("state-table")
	stateRegion, _ := cmd.Flags().GetString("state-region")

	// ── Resolve project directory ─────────────────────────────────────────────
	projectDir, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolve project directory: %w", err)
	}

	// ── Build PipelineContext ─────────────────────────────────────────────────
	pctx := &pipeline.PipelineContext{
		ProjectDir:  projectDir,
		AWSRegion:   region,
		AWSProfile:  profile,
		AppName:     name,
		TagOverride: tag,
		AllowPublic: allowPublic,
		DryRun:      dryRun,
		Destroy:     destroy,
		SkipConfirm: yes,
	}

	// ── AWS config ────────────────────────────────────────────────────────────
	awsOpts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if profile != "" {
		awsOpts = append(awsOpts, awsconfig.WithSharedConfigProfile(profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsOpts...)
	if err != nil {
		return fmt.Errorf("AWS config (profile=%s, region=%s): %w", profile, region, err)
	}

	// ── Docker pinger (shell-out to avoid importing the Docker SDK) ───────────
	dockerPinger := workspace.DockerPingerFunc(func(ctx context.Context) error {
		out, err := exec.CommandContext(ctx, "docker", "info").CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker info: %w\n  Output: %s", err, string(out))
		}
		return nil
	})

	// ── Remote State Configuration (vessel.json) ─────────────────────────────────
	projCfg, err := workspace.LoadProjectConfig(projectDir)
	if err != nil {
		return fmt.Errorf("load vessel.json: %w", err)
	}

	cfgChanged := false
	if stateBucket != "" || stateTable != "" || stateRegion != "" {
		if projCfg.RemoteState == nil {
			projCfg.RemoteState = &types.RemoteStateConfig{}
		}
		if stateBucket != "" {
			projCfg.RemoteState.Bucket = stateBucket
		}
		if stateTable != "" {
			projCfg.RemoteState.Table = stateTable
		}
		if stateRegion != "" {
			projCfg.RemoteState.Region = stateRegion
		} else if projCfg.RemoteState.Region == "" {
			projCfg.RemoteState.Region = region
		}
		cfgChanged = true
	}

	if cfgChanged {
		if err := workspace.SaveProjectConfig(projectDir, projCfg); err != nil {
			return fmt.Errorf("save vessel.json: %w", err)
		}
		fmt.Printf("Updated vessel.json with remote state configuration\n")
	}

	pctx.RemoteState = projCfg.RemoteState

	// ── State manager (load before Preflight to retrieve cached CIDR) ─────────
	stateMgr := workspace.NewStateManager()
	state, err := stateMgr.Load(ctx, projectDir, pctx.RemoteState)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// ── Merge environment variables: state.json baseline + CLI overrides ───────
	// Start with persisted vars from the previous deploy, then apply any new
	// --env flags on top. An empty value (KEY=) removes the key.
	envVars := make(map[string]string)
	for k, v := range state.EnvVars {
		envVars[k] = v
	}
	for _, kv := range envFlags {
		idx := strings.Index(kv, "=")
		if idx < 0 {
			return fmt.Errorf("invalid --env %q: must be KEY=VALUE", kv)
		}
		key, val := kv[:idx], kv[idx+1:]
		if val == "" {
			delete(envVars, key) // KEY= unsets the variable
		} else {
			envVars[key] = val
		}
	}
	// Apply merged env vars to the pipeline context.
	if len(envVars) > 0 {
		pctx.EnvVars = envVars
	}

	// ── Merge CPU / Memory / Port: CLI flag > state.json > built-in default ────
	// A zero CLI flag means "not set"; fall back to the persisted value, then
	// the built-in default. Values are re-persisted on every successful deploy.
	const defaultCPU, defaultMemory, defaultPort = 256, 512, 8080
	pctx.CPU = mergeInt(cpuFlag, state.CPU, defaultCPU)
	pctx.Memory = mergeInt(memoryFlag, state.Memory, defaultMemory)
	pctx.Port = mergeInt(portFlag, state.Port, defaultPort)

	// ── Merge ALB flags: CLI flag > state.json ───────────────────────────────────
	// LoadBalancer is sticky: once set true via CLI, it persists in state.json
	// so subsequent re-deploys keep the ALB without re-passing the flag.
	pctx.LoadBalancer = lbFlag || state.LoadBalancer
	// CertificateARN: CLI flag wins; fall back to persisted value.
	if certARNFlag != "" {
		pctx.CertificateARN = certARNFlag
	} else {
		pctx.CertificateARN = state.CertificateARN
	}

	// ── Assemble Preflight with all 6 real checks ─────────────────────────────
	preflight := workspace.NewPreflight(workspace.PreflightOptions{
		DockerPinger: dockerPinger,
		TFChecker:    workspace.TFVersionCheckerFunc(workspace.CheckTerraformVersion),
		STSClient:    sts.NewFromConfig(awsCfg),
		ECRClient:    awsecr.NewFromConfig(awsCfg),
		EC2Client:    ec2.NewFromConfig(awsCfg),
		IPDetector:   workspace.IPDetectorFunc(workspace.CheckIPHTTP),
		CachedCIDR:   state.CachedCIDR,
		AllowPublic:  allowPublic,
	})

	// ── Wire orchestrator ─────────────────────────────────────────────────────
	orch := pipeline.NewOrchestrator(pipeline.OrchestratorConfig{
		Preflight:  preflight,
		Workspace:  workspace.NewManager(projectDir),
		Inspector:  workspace.NewInspector(),
		Generator:  artifact.NewGenerator(),
		Compiler:   docker.NewCompiler(),
		Renderer:   terraform.NewRenderer(),
		Executor:   terraform.NewExecutor(),
		Deployer:   ecs.NewDeployer(region, profile),
		ECRCleaner: internalecr.NewCleaner(region, profile),
		StateMgr:   stateMgr,
		UI:         ui.NewDefault(),
	})

	return orch.Run(ctx, pctx)
}

// mergeInt returns the first non-zero value among cli, persisted, and fallback.
// This gives CLI flags the highest priority, then persisted state, then the default.
func mergeInt(cli, persisted, fallback int) int {
	if cli != 0 {
		return cli
	}
	if persisted != 0 {
		return persisted
	}
	return fallback
}
