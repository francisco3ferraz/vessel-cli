package terraform_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tfpkg "github.com/francisco3ferraz/vessel-cli/internal/terraform"
	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// ─── Compile-time interface check ─────────────────────────────────────────────
var _ ports.IaCRenderer = &tfpkg.Renderer{}

// mkWorkspace creates a temp dir simulating a vessel-cli workspace.
// .vessel-cli/tf/ is pre-created because the workspace manager would
// normally do this before the renderer is called.
func mkWorkspace(t *testing.T) (projectDir, tfDir string) {
	t.Helper()
	projectDir = t.TempDir()
	tfDir = filepath.Join(projectDir, ".vessel-cli", "tf")
	if err := os.MkdirAll(tfDir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return projectDir, tfDir
}

func basePctx(projectDir string) *types.PipelineContext {
	return &types.PipelineContext{
		ProjectDir:    projectDir,
		AppName:       "my-app",
		AWSRegion:     "us-east-1",
		ImageTag:      "my-app:abc12345",
		CallerIP:      "1.2.3.4/32",
		IsFirstDeploy: true,
	}
}

// readTF reads a generated file from .vessel-cli/tf/.
func readTF(t *testing.T, tfDir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tfDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// ─── main.tf ──────────────────────────────────────────────────────────────────

func TestRenderer_MainTF_DesiredCountZeroOnFirstDeploy(t *testing.T) {
	projectDir, tfDir := mkWorkspace(t)
	pctx := basePctx(projectDir)
	pctx.IsFirstDeploy = true

	tfpkg.NewRenderer().Render(context.Background(), pctx, ports.BackendConfig{Type: ports.BackendTypeLocal})

	main := readTF(t, tfDir, "main.tf")
	if !strings.Contains(main, "desired_count   = 0") {
		t.Errorf("expected desired_count = 0 on first deploy\n%s", main)
	}
}

func TestRenderer_MainTF_DesiredCountOneOnSubsequentDeploy(t *testing.T) {
	projectDir, tfDir := mkWorkspace(t)
	pctx := basePctx(projectDir)
	pctx.IsFirstDeploy = false

	tfpkg.NewRenderer().Render(context.Background(), pctx, ports.BackendConfig{})

	main := readTF(t, tfDir, "main.tf")
	if !strings.Contains(main, "desired_count   = 1") {
		t.Errorf("expected desired_count = 1 on subsequent deploy\n%s", main)
	}
}

func TestRenderer_MainTF_ContainsRequiredResources(t *testing.T) {
	projectDir, tfDir := mkWorkspace(t)
	tfpkg.NewRenderer().Render(context.Background(), basePctx(projectDir), ports.BackendConfig{})

	main := readTF(t, tfDir, "main.tf")
	for _, want := range []string{
		`aws_security_group`,
		`aws_iam_role`,
		`aws_cloudwatch_log_group`,
		`aws_ecr_repository`,
		`aws_ecs_cluster`,
		`aws_ecs_task_definition`,
		`aws_ecs_service`,
		`data.aws_vpc.default`,
		`data.aws_subnets.public`,
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.tf missing %q", want)
		}
	}
}

// ─── variables.tf ─────────────────────────────────────────────────────────────

func TestRenderer_VariablesTF_ContainsAllVariables(t *testing.T) {
	projectDir, tfDir := mkWorkspace(t)
	tfpkg.NewRenderer().Render(context.Background(), basePctx(projectDir), ports.BackendConfig{})

	vars := readTF(t, tfDir, "variables.tf")
	for _, want := range []string{`variable "app_name"`, `variable "aws_region"`, `variable "image_tag"`, `variable "allowed_cidr"`} {
		if !strings.Contains(vars, want) {
			t.Errorf("variables.tf missing %q", want)
		}
	}
}

func TestRenderer_VariablesTF_AllowedCIDRHasNoDefault(t *testing.T) {
	projectDir, tfDir := mkWorkspace(t)
	tfpkg.NewRenderer().Render(context.Background(), basePctx(projectDir), ports.BackendConfig{})

	vars := readTF(t, tfDir, "variables.tf")
	// Find the allowed_cidr block and ensure it has no default = line.
	cidrIdx := strings.Index(vars, `variable "allowed_cidr"`)
	if cidrIdx < 0 {
		t.Fatal("allowed_cidr variable not found")
	}
	// The block closes at the next `}` after the variable declaration.
	blockEnd := strings.Index(vars[cidrIdx:], "\n}") + cidrIdx
	block := vars[cidrIdx : blockEnd+2]
	if strings.Contains(block, "default =") {
		t.Errorf("allowed_cidr must NOT have a default = value (vessel-cli always supplies it explicitly)\nBlock:\n%s", block)
	}
}

// ─── outputs.tf ───────────────────────────────────────────────────────────────

func TestRenderer_OutputsTF_ContainsAllOutputs(t *testing.T) {
	projectDir, tfDir := mkWorkspace(t)
	tfpkg.NewRenderer().Render(context.Background(), basePctx(projectDir), ports.BackendConfig{})

	out := readTF(t, tfDir, "outputs.tf")
	for _, want := range []string{`"ecr_repository_uri"`, `"ecs_cluster_arn"`, `"ecs_service_arn"`, `"ecs_task_def_arn"`} {
		if !strings.Contains(out, want) {
			t.Errorf("outputs.tf missing %q", want)
		}
	}
}

// ─── terraform.tfvars ────────────────────────────────────────────────────────

func TestRenderer_TFVars_ContainsCorrectValues(t *testing.T) {
	projectDir, tfDir := mkWorkspace(t)
	tfpkg.NewRenderer().Render(context.Background(), basePctx(projectDir), ports.BackendConfig{})

	tfvars := readTF(t, tfDir, "terraform.tfvars")
	for _, want := range []string{
		`app_name     = "my-app"`,
		`aws_region   = "us-east-1"`,
		`image_tag    = "my-app:abc12345"`,
		`allowed_cidr = "1.2.3.4/32"`,
	} {
		if !strings.Contains(tfvars, want) {
			t.Errorf("terraform.tfvars missing %q\n%s", want, tfvars)
		}
	}
}

// ─── TFWorkDir and idempotency ────────────────────────────────────────────────

func TestRenderer_SetsTFWorkDir(t *testing.T) {
	projectDir, tfDir := mkWorkspace(t)
	pctx := basePctx(projectDir)
	tfpkg.NewRenderer().Render(context.Background(), pctx, ports.BackendConfig{})

	if pctx.TFWorkDir != tfDir {
		t.Errorf("expected TFWorkDir=%s, got %s", tfDir, pctx.TFWorkDir)
	}
}

func TestRenderer_Idempotent(t *testing.T) {
	projectDir, tfDir := mkWorkspace(t)
	pctx := basePctx(projectDir)
	r := tfpkg.NewRenderer()

	r.Render(context.Background(), pctx, ports.BackendConfig{})
	first, _ := os.ReadFile(filepath.Join(tfDir, "main.tf"))

	r.Render(context.Background(), pctx, ports.BackendConfig{})
	second, _ := os.ReadFile(filepath.Join(tfDir, "main.tf"))

	if string(first) != string(second) {
		t.Error("renderer is not idempotent: second render produced a different main.tf")
	}
}
