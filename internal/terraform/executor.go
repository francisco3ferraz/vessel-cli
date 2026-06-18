package terraform

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// Executor implements ports.TerraformExecutor using the terraform CLI.
// Consistent with the docker compiler: no SDK dependency, exec.Command only.
type Executor struct{}

// NewExecutor returns a new Executor.
func NewExecutor() *Executor { return &Executor{} }

// compile-time interface guard
var _ ports.TerraformExecutor = &Executor{}

// Init runs `terraform init -input=false -upgrade=false` in workDir,
// streaming all output to logWriter.
func (e *Executor) Init(ctx context.Context, workDir string, logWriter io.Writer) error {
	cmd := exec.CommandContext(ctx, "terraform", "init", "-input=false", "-upgrade=false")
	cmd.Dir = workDir
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}
	return nil
}

// Apply runs `terraform apply -auto-approve -input=false` in workDir,
// streams output to logWriter, and populates pctx.CloudOutputs on success.
func (e *Executor) Apply(ctx context.Context, workDir string, pctx *types.PipelineContext, logWriter io.Writer) error {
	cmd := exec.CommandContext(ctx, "terraform", "apply", "-auto-approve", "-input=false")
	cmd.Dir = workDir
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("terraform apply: %w", err)
	}

	// Parse outputs immediately after apply so pctx is enriched for subsequent stages.
	outputs, err := e.Output(ctx, workDir)
	if err != nil {
		return fmt.Errorf("read terraform outputs: %w", err)
	}
	pctx.CloudOutputs = *outputs
	return nil
}

// Output runs `terraform output -json` in workDir and returns typed CloudOutputs.
func (e *Executor) Output(ctx context.Context, workDir string) (*types.CloudOutputs, error) {
	cmd := exec.CommandContext(ctx, "terraform", "output", "-json")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("terraform output -json: %w", err)
	}
	return ParseOutputJSON(out)
}

// Destroy runs `terraform destroy -auto-approve -input=false` in workDir,
// streaming all output to logWriter.
func (e *Executor) Destroy(ctx context.Context, workDir string, logWriter io.Writer) error {
	cmd := exec.CommandContext(ctx, "terraform", "destroy", "-auto-approve", "-input=false")
	cmd.Dir = workDir
	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("terraform destroy: %w", err)
	}
	return nil
}
