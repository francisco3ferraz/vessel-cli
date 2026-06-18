// Package terraform implements the IaCRenderer and TerraformExecutor ports.
// The renderer generates Terraform HCL files from embedded templates;
// the executor (Phase 3b) wraps the terraform binary via tfexec.
package terraform

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

//go:embed templates
var templateFS embed.FS

// TerraformSpec is the data projected into the .tf templates.
// It is derived from PipelineContext at render time and is the sole input
// to the template engine — the templates never import Go types directly.
type TerraformSpec struct {
	AppName      string
	AWSRegion    string
	ImageTag     string
	AllowedCIDR  string
	DesiredCount int // 0 on first deploy (pre-push), 1 on subsequent deploys
}

// Renderer implements ports.IaCRenderer.
type Renderer struct{}

// NewRenderer returns a new Renderer.
func NewRenderer() *Renderer { return &Renderer{} }

// Render generates main.tf, variables.tf, outputs.tf, and terraform.tfvars
// into .vessel-cli/tf/ and sets pctx.TFWorkDir on success.
//
// Idempotent: same inputs always produce identical output files.
// The backend parameter is accepted for interface compatibility; in v1 only
// the local backend is used (no explicit backend block = Terraform default).
func (r *Renderer) Render(_ context.Context, pctx *types.PipelineContext, _ ports.BackendConfig) error {
	spec := TerraformSpec{
		AppName:      pctx.AppName,
		AWSRegion:    pctx.AWSRegion,
		// ImageTag must be ONLY the tag suffix (e.g. "abc12345" or "local"),
		// NOT the full local name:tag. The ECR image URL is constructed in
		// main.tf as: "${aws_ecr_repository.app.repository_url}:${var.image_tag}"
		// Passing "vessel-cli:local" here would produce an invalid double-colon URL.
		ImageTag:     ecrTagSuffix(pctx.ImageTag),
		AllowedCIDR: pctx.CallerIP,
		DesiredCount: desiredCount(pctx.IsFirstDeploy),
	}

	workDir := pctx.TFWorkDir
	if workDir == "" {
		workDir = filepath.Join(pctx.ProjectDir, ".vessel-cli", "tf")
	}

	renders := []struct {
		tmpl string
		out  string
	}{
		{"templates/main.tf.tmpl", "main.tf"},
		{"templates/variables.tf.tmpl", "variables.tf"},
		{"templates/outputs.tf.tmpl", "outputs.tf"},
		{"templates/terraform.tfvars.tmpl", "terraform.tfvars"},
	}

	for _, entry := range renders {
		if err := renderFile(entry.tmpl, filepath.Join(workDir, entry.out), spec); err != nil {
			return fmt.Errorf("render %s: %w", entry.out, err)
		}
	}

	pctx.TFWorkDir = workDir
	return nil
}

// renderFile parses one embedded template and writes the result to outPath.
func renderFile(tmplPath, outPath string, spec TerraformSpec) error {
	tmpl, err := template.ParseFS(templateFS, tmplPath)
	if err != nil {
		return fmt.Errorf("parse %s: %w", tmplPath, err)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	if err := tmpl.Execute(f, spec); err != nil {
		return fmt.Errorf("execute %s: %w", tmplPath, err)
	}
	return nil
}

// desiredCount maps the first-deploy flag to the ECS service desired_count.
func desiredCount(isFirstDeploy bool) int {
	if isFirstDeploy {
		return 0
	}
	return 1
}

// ecrTagSuffix extracts the tag portion from a local Docker image tag so it
// can be used safely in an ECR image URL.
//
//	"vessel-cli:abc12345" → "abc12345"
//	"vessel-cli:local"    → "local"
//	"vessel-cli"          → "vessel-cli"  (no colon: treat whole string as tag)
func ecrTagSuffix(imageTag string) string {
	if idx := strings.LastIndex(imageTag, ":"); idx >= 0 {
		return imageTag[idx+1:]
	}
	return imageTag
}
