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

	"github.com/francisco3ferraz/vessel-cli/internal/ui"
	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// Orchestrator sequences the deployment pipeline stages.
//
// In Phase 1: Stage 0 (Preflight) and Stage 1 (Inspect) are fully wired.
// Stages 2-6 accept nil values and print a stub message until Phase 2-4.
type Orchestrator struct {
	preflight ports.PreflightChecker
	workspace ports.WorkspaceInitializer
	inspector ports.WorkspaceInspector
	generator ports.ArtifactGenerator // nil until Phase 2
	compiler  ports.DockerCompiler    // nil until Phase 2
	renderer  ports.IaCRenderer       // nil until Phase 3
	executor  ports.TerraformExecutor // nil until Phase 3
	stateMgr  ports.StateManager
	ui        *ui.Renderer
}

// OrchestratorConfig holds all injected dependencies.
// nil values for unimplemented stages (Generator, Compiler, Renderer, Executor)
// are explicitly allowed in Phase 1.
type OrchestratorConfig struct {
	Preflight ports.PreflightChecker
	Workspace ports.WorkspaceInitializer
	Inspector ports.WorkspaceInspector
	Generator ports.ArtifactGenerator
	Compiler  ports.DockerCompiler
	Renderer  ports.IaCRenderer
	Executor  ports.TerraformExecutor
	StateMgr  ports.StateManager
	UI        *ui.Renderer
}

// NewOrchestrator constructs an Orchestrator from the provided config.
func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	return &Orchestrator{
		preflight: cfg.Preflight,
		workspace: cfg.Workspace,
		inspector: cfg.Inspector,
		generator: cfg.Generator,
		compiler:  cfg.Compiler,
		renderer:  cfg.Renderer,
		executor:  cfg.Executor,
		stateMgr:  cfg.StateMgr,
		ui:        cfg.UI,
	}
}

// Run executes the deployment pipeline against the provided PipelineContext.
func (o *Orchestrator) Run(ctx context.Context, pctx *types.PipelineContext) error {
	// ── Workspace init & lock ─────────────────────────────────────────────────
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
	if o.compiler != nil {
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

		o.ui.StartStage("Terraform", "terraform apply  (this may take ~90 s on first run)")
		if err := o.executor.Apply(ctx, pctx.TFWorkDir, pctx, lw); err != nil {
			o.ui.FailStage(err)
			return err
		}
		o.ui.CompleteStage(fmt.Sprintf("ECR: %s", pctx.CloudOutputs.ECRRepositoryURI))
	}

	// ── Save partial state ────────────────────────────────────────────────────
	newState := &types.DeploymentState{
		AppName:          pctx.AppName,
		AWSRegion:        pctx.AWSRegion,
		CachedCIDR:       pctx.CallerIP,
		LastImageTag:     pctx.ImageTag,
		ECRRepositoryURI: pctx.CloudOutputs.ECRRepositoryURI,
		ECSClusterARN:    pctx.CloudOutputs.ECSClusterARN,
		ECSServiceARN:    pctx.CloudOutputs.ECSServiceARN,
	}
	if err := o.stateMgr.Save(pctx.ProjectDir, newState); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	o.ui.Log("State saved → .vessel-cli/state.json")

	return nil
}
