package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/francisco3ferraz/vessel-cli/internal/artifact"
	"github.com/francisco3ferraz/vessel-cli/internal/docker"
	"github.com/francisco3ferraz/vessel-cli/internal/pipeline"
	"github.com/francisco3ferraz/vessel-cli/internal/terraform"
	"github.com/francisco3ferraz/vessel-cli/internal/ui"
	"github.com/francisco3ferraz/vessel-cli/internal/workspace"
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
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithSharedConfigProfile(profile),
	)
	if err != nil {
		return fmt.Errorf("AWS config (profile=%s, region=%s): %w", profile, region, err)
	}

	// ── Docker pinger (shell-out; Docker SDK import deferred to Phase 2) ──────
	// We run `docker info` rather than importing the Docker SDK client so that
	// Phase 1 compiles without the docker/docker SDK as a transitive dependency.
	// The full SDK client is injected in Phase 2 when DockerCompiler is wired.
	dockerPinger := workspace.DockerPingerFunc(func(ctx context.Context) error {
		out, err := exec.CommandContext(ctx, "docker", "info").CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker info: %w\n  Output: %s", err, string(out))
		}
		return nil
	})

	// ── State manager (load before Preflight to retrieve cached CIDR) ─────────
	stateMgr := workspace.NewStateManager()
	state, err := stateMgr.Load(projectDir)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// ── Assemble Preflight with all 6 real checks ─────────────────────────────
	preflight := workspace.NewPreflight(workspace.PreflightOptions{
		DockerPinger: dockerPinger,
		TFChecker:    workspace.TFVersionCheckerFunc(workspace.CheckTerraformVersion),
		STSClient:    sts.NewFromConfig(awsCfg),
		ECRClient:    ecr.NewFromConfig(awsCfg),
		EC2Client:    ec2.NewFromConfig(awsCfg),
		IPDetector:   workspace.IPDetectorFunc(workspace.CheckIPHTTP),
		CachedCIDR:   state.CachedCIDR,
		AllowPublic:  allowPublic,
	})

	// ── Wire orchestrator ─────────────────────────────────────────────────────
	orch := pipeline.NewOrchestrator(pipeline.OrchestratorConfig{
		Preflight: preflight,
		Workspace: workspace.NewManager(projectDir),
		Inspector: workspace.NewInspector(),
		Generator: artifact.NewGenerator(),
		Compiler:  docker.NewCompiler(),
		Renderer:  terraform.NewRenderer(),
		StateMgr:  stateMgr,
		UI:        ui.NewDefault(),
		// Executor: nil until Phase 3b.
	})

	return orch.Run(ctx, pctx)
}
