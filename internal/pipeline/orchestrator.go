// Package pipeline owns the orchestration layer of the deployment pipeline.
// The Orchestrator sequences the stages and owns the PipelineContext envelope.
//
// Dependency rule: this package imports ONLY pkg/ports interfaces and internal/ui.
// It NEVER imports internal/workspace, internal/docker, or internal/terraform directly.
// Concrete adapters are injected at startup in cmd/deploy.go.
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/francisco3ferraz/vessel-cli/internal/ui"
	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// Orchestrator sequences the seven deployment pipeline stages.
// All stages are wired. Nil guards remain for forward-compatibility only.
type Orchestrator struct {
	preflight  ports.PreflightChecker
	workspace  ports.WorkspaceInitializer
	inspector  ports.WorkspaceInspector
	generator  ports.ArtifactGenerator
	compiler   ports.DockerCompiler
	renderer   ports.IaCRenderer
	executor   ports.TerraformExecutor
	deployer   ports.Deployer
	ecrCleaner ports.ECRCleaner
	stateMgr   ports.StateManager
	ui         *ui.Renderer
}

// OrchestratorConfig holds all injected dependencies.
type OrchestratorConfig struct {
	Preflight  ports.PreflightChecker
	Workspace  ports.WorkspaceInitializer
	Inspector  ports.WorkspaceInspector
	Generator  ports.ArtifactGenerator
	Compiler   ports.DockerCompiler
	Renderer   ports.IaCRenderer
	Executor   ports.TerraformExecutor
	Deployer   ports.Deployer
	ECRCleaner ports.ECRCleaner
	StateMgr   ports.StateManager
	UI         *ui.Renderer
}

// NewOrchestrator constructs an Orchestrator from the provided config.
func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	return &Orchestrator{
		preflight:  cfg.Preflight,
		workspace:  cfg.Workspace,
		inspector:  cfg.Inspector,
		generator:  cfg.Generator,
		compiler:   cfg.Compiler,
		renderer:   cfg.Renderer,
		executor:   cfg.Executor,
		deployer:   cfg.Deployer,
		ecrCleaner: cfg.ECRCleaner,
		stateMgr:   cfg.StateMgr,
		ui:         cfg.UI,
	}
}

// Run executes either the deploy or destroy pipeline depending on pctx flags.
func (o *Orchestrator) Run(ctx context.Context, pctx *types.PipelineContext) error {

	if pctx.Destroy {
		return o.runDestroy(ctx, pctx)
	}
	return o.runDeploy(ctx, pctx)
}

// runDestroy tears down all cloud resources via `terraform destroy` and
// removes .vessel-cli/state.json. It does NOT need Docker or preflight.
func (o *Orchestrator) runDestroy(ctx context.Context, pctx *types.PipelineContext) error {
	if err := o.workspace.Init(); err != nil {
		return fmt.Errorf("workspace init: %w", err)
	}
	if err := o.workspace.AcquireLock(); err != nil {
		return err
	}
	defer o.workspace.ReleaseLock()

	// Confirm a deployment exists before attempting destroy.
	state, err := o.stateMgr.Load(pctx.ProjectDir)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state.AppName == "" {
		return fmt.Errorf(
			"no deployment found — nothing to destroy\n" +
				"  Run vessel-cli deploy first",
		)
	}

	// Populate pctx from state (preflight + inspect are skipped in destroy path).
	pctx.AppName = state.AppName
	pctx.AWSRegion = state.AWSRegion
	pctx.CallerIP = state.CachedCIDR
	pctx.ImageTag = state.LastImageTag
	pctx.TFWorkDir = filepath.Join(pctx.ProjectDir, ".vessel-cli", "tf")

	// ── Step 1: Delete ECR images so terraform destroy can remove the repo ────
	if o.ecrCleaner != nil && state.ECRRepositoryURI != "" {
		repoName := ecrRepoName(state.ECRRepositoryURI)
		o.ui.StartStage("Cleanup", fmt.Sprintf("Deleting ECR images from %s", repoName))
		if err := o.ecrCleaner.DeleteAllImages(ctx, state.AWSRegion, repoName); err != nil {
			o.ui.FailStage(err)
			return err
		}
		o.ui.CompleteStage("ECR repository emptied")
	}
	// Re-render templates in destroy mode so ECR lifecycle guards are removed.
	if o.renderer != nil {
		if err := o.renderer.Render(ctx, pctx, ports.BackendConfig{Type: ports.BackendTypeLocal}); err != nil {
			return fmt.Errorf("re-render templates for destroy: %w", err)
		}
	}

	lw := &tfLogWriter{ui: o.ui}

	// Re-init so Terraform picks up the updated config (no-op if already init'd).
	if o.executor != nil {
		if err := o.executor.Init(ctx, pctx.TFWorkDir, lw); err != nil {
			return fmt.Errorf("terraform init: %w", err)
		}
	}

	o.ui.StartStage("Destroy", fmt.Sprintf("terraform destroy  (removing %s infrastructure)", state.AppName))
	if err := o.executor.Destroy(ctx, pctx.TFWorkDir, lw); err != nil {
		o.ui.FailStage(err)
		return err
	}
	o.ui.CompleteStage(fmt.Sprintf("All cloud resources for %q removed", state.AppName))

	if err := o.stateMgr.Delete(pctx.ProjectDir); err != nil {
		// Non-fatal — log a warning but don't fail the command.
		o.ui.Log("warning: could not remove state.json: %v", err)
	} else {
		o.ui.Log(".vessel-cli/state.json removed")
	}
	return nil
}

// runDeploy is the full deploy pipeline (stages 0–7).
func (o *Orchestrator) runDeploy(ctx context.Context, pctx *types.PipelineContext) error {
	if err := o.workspace.Init(); err != nil {
		return fmt.Errorf("workspace init: %w", err)
	}
	if err := o.workspace.AcquireLock(); err != nil {
		return err
	}
	defer o.workspace.ReleaseLock()

	// ── Load state → determine IsFirstDeploy ──────────────────────────────────
	state, err := o.stateMgr.Load(pctx.ProjectDir)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	pctx.IsFirstDeploy = state.AppName == ""
	// On subsequent deploys, restore CloudOutputs from state so Stage 6
	// (Push) has the ECR URI even if Stage 5 hasn't run yet.
	if state.ECRRepositoryURI != "" {
		pctx.CloudOutputs.ECRRepositoryURI = state.ECRRepositoryURI
		pctx.CloudOutputs.ECSClusterARN = state.ECSClusterARN
		pctx.CloudOutputs.ECSServiceARN = state.ECSServiceARN
	}

	// ── Stage 0: Preflight ────────────────────────────────────────────────────
	o.ui.StartStage("Preflight", "Checking environment")
	if err := o.preflight.Check(ctx, pctx); err != nil {
		o.ui.FailStage(err)
		return err
	}
	o.ui.CompleteStage(fmt.Sprintf(
		"All checks passed  (account: %s, region: %s)", pctx.AWSAccountID, pctx.AWSRegion,
	))

	// ── Stage 1: Workspace inspection ─────────────────────────────────────────
	o.ui.StartStage("Inspect", "Analyzing Go project")
	if err := o.inspector.Inspect(ctx, pctx); err != nil {
		o.ui.FailStage(err)
		return err
	}
	// AppName from --name flag takes precedence; fall back to module binary name.
	if pctx.AppName == "" {
		pctx.AppName = pctx.BinaryName
	}
	o.ui.CompleteStage(fmt.Sprintf(
		"Module: %s  Go: %s  Tag: %s", pctx.ModuleName, pctx.GoVersion, pctx.ImageTag,
	))
	if pctx.IsFirstDeploy {
		o.ui.Log("First deploy — no existing state.json found")
	}

	// ── Stage 2: Artifact generation ──────────────────────────────────────────
	if o.generator != nil {
		o.ui.StartStage("Generate", "Rendering Dockerfile")
		if err := o.generator.GenerateDockerfile(ctx, pctx); err != nil {
			o.ui.FailStage(err)
			return err
		}
		o.ui.CompleteStage(fmt.Sprintf("Dockerfile → .vessel-cli/Dockerfile"))
	}

	// ── Stage 3: Docker build ──────────────────────────────────────────────────
	if o.compiler != nil && !pctx.DryRun {
		o.ui.StartStage("Build", fmt.Sprintf("docker build -t %s", pctx.ImageTag))
		events := make(chan ports.BuildEvent, 128)
		errCh := make(chan error, 1)
		go func() {
			errCh <- o.compiler.Build(ctx, pctx, events)
			close(events)
		}()
		for event := range events {
			switch event.Type {
			case ports.BuildEventLog, ports.BuildEventStep:
				o.ui.Log("%s", event.Message)
			}
		}
		if err := <-errCh; err != nil {
			o.ui.FailStage(err)
			return err
		}
		id := pctx.ImageID
		if len(id) > 12 {
			id = id[:12]
		}
		o.ui.CompleteStage(fmt.Sprintf("Image: %s  (id: %s)", pctx.ImageTag, id))
	}

	// ── Stage 4: IaC render ───────────────────────────────────────────────────
	if o.renderer != nil {
		o.ui.StartStage("Render", "Generating Terraform files")
		backend := ports.BackendConfig{Type: ports.BackendTypeLocal}
		if err := o.renderer.Render(ctx, pctx, backend); err != nil {
			o.ui.FailStage(err)
			return err
		}
		o.ui.CompleteStage(fmt.Sprintf("IaC → %s", pctx.TFWorkDir))
	}

	// ── Stage 5: Terraform init + apply ──────────────────────────────────────
	if o.executor != nil {
		lw := &tfLogWriter{ui: o.ui}

		o.ui.StartStage("Terraform", "terraform init")
		if err := o.executor.Init(ctx, pctx.TFWorkDir, lw); err != nil {
			o.ui.FailStage(err)
			return err
		}
		o.ui.CompleteStage("init complete")

		if pctx.DryRun {
			o.ui.StartStage("Terraform", "terraform plan")
			if err := o.executor.Plan(ctx, pctx.TFWorkDir, lw); err != nil {
				o.ui.FailStage(err)
				return err
			}
			o.ui.CompleteStage("dry run complete")
			return nil
		}

		o.ui.StartStage("Terraform", "terraform apply  (this may take ~90 s on first run)")
		if err := o.executor.Apply(ctx, pctx.TFWorkDir, pctx, lw); err != nil {
			o.ui.FailStage(err)
			return err
		}
		o.ui.CompleteStage(fmt.Sprintf("ECR: %s", pctx.CloudOutputs.ECRRepositoryURI))
	}

	// ── Stage 6: ECR push ───────────────────────────────────────────────────
	if o.compiler != nil {
		o.ui.StartStage("Push", "Pushing image to ECR")
		pushEvents := make(chan ports.BuildEvent, 128)
		pushErrCh := make(chan error, 1)
		go func() {
			pushErrCh <- o.compiler.Push(ctx, pctx, pushEvents)
			close(pushEvents)
		}()
		for event := range pushEvents {
			switch event.Type {
			case ports.BuildEventPush, ports.BuildEventLog:
				o.ui.Log("%s", event.Message)
			}
		}
		if err := <-pushErrCh; err != nil {
			o.ui.FailStage(err)
			return err
		}
		o.ui.CompleteStage("Image pushed")
	}

	// ── Stage 7: ECS scale-up ──────────────────────────────────────────────────
	if o.deployer != nil {
		// Guard: both ARNs must be populated — if terraform outputs were
		// incomplete (e.g. partial apply), give a clear error rather than
		// sending empty strings to the AWS API.
		if pctx.CloudOutputs.ECSClusterARN == "" || pctx.CloudOutputs.ECSServiceARN == "" {
			return fmt.Errorf(
				"ECS cluster/service ARNs are empty after terraform apply\n" +
					"  Check the ECS console or .vessel-cli/tf/terraform.tfstate",
			)
		}
		o.ui.StartStage("Deploy", "ecs:UpdateService → desired_count=1  (waiting up to 5 min)")
		if err := o.deployer.Scale(
			ctx,
			pctx.CloudOutputs.ECSClusterARN,
			pctx.CloudOutputs.ECSServiceARN,
			1,
		); err != nil {
			o.ui.FailStage(err)
			return err
		}
		o.ui.CompleteStage("✅  Service stable — your app is running on ECS Fargate")
	}

	// ── Save state ───────────────────────────────────────────────────────────────
	newState := &types.DeploymentState{
		AppName:          pctx.AppName,
		AWSRegion:        pctx.AWSRegion,
		CachedCIDR:       pctx.CallerIP,
		LastImageTag:     pctx.ImageTag,
		ECRRepositoryURI: pctx.CloudOutputs.ECRRepositoryURI,
		ECSClusterARN:    pctx.CloudOutputs.ECSClusterARN,
		ECSServiceARN:    pctx.CloudOutputs.ECSServiceARN,
		EnvVars:          pctx.EnvVars,
		CPU:              pctx.CPU,
		Memory:           pctx.Memory,
		Port:             pctx.Port,
		LoadBalancer:     pctx.LoadBalancer,
		CertificateARN:   pctx.CertificateARN,
		ALBDNSName:       pctx.CloudOutputs.ALBDNSName,
		// LastDeployedAt is stamped by StateManager.Save() — not set here.
	}
	if err := o.stateMgr.Save(pctx.ProjectDir, newState); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	o.ui.Log("State saved → .vessel-cli/state.json")

	return nil
}

// ecrRepoName extracts the repository name from a full ECR URI.
// "123456789.dkr.ecr.us-east-1.amazonaws.com/my-app" → "my-app"
func ecrRepoName(uri string) string {
	parts := strings.SplitN(uri, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return uri
}
