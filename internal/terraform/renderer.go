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
	"sort"
	"strconv"
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
	AppName        string
	AWSRegion      string
	ImageTag       string // ECR tag suffix only (e.g. "abc12345", not "app:abc12345")
	AllowedCIDR    string
	DesiredCount   int
	CPU            int
	Memory         int
	Port           int
	LoadBalancer   bool
	CertificateARN string
	EnvVars        []EnvVar    // sorted by Name for deterministic output
	SecretVars     []SecretVar // sorted by Name for deterministic output
	// IsDestroy removes lifecycle guards (e.g. prevent_destroy on ECR)
	// so `terraform destroy` can succeed. Set only by the destroy path.
	IsDestroy bool
	Backend   *ports.BackendConfig
}

// EnvVar is a single name/value environment variable pair (plain, not secret).
type EnvVar struct {
	Name  string
	Value string
}

// SecretVar is a Secrets Manager reference injected into the ECS task definition
// via the `secrets` block. The container receives the value as an env var at
// runtime; the value itself never appears in Terraform HCL.
type SecretVar struct {
	Name      string // environment variable name inside the container
	ValueFrom string // full Secrets Manager ARN
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
func (r *Renderer) Render(_ context.Context, pctx *types.PipelineContext, backend ports.BackendConfig) error {
	spec := TerraformSpec{
		AppName:        pctx.AppName,
		AWSRegion:      pctx.AWSRegion,
		// ImageTag must be ONLY the tag suffix (e.g. "abc12345" or "local"),
		// NOT the full local name:tag. The ECR image URL is constructed in
		// main.tf as: "${aws_ecr_repository.app.repository_url}:${var.image_tag}"
		// Passing "vessel-cli:local" here would produce an invalid double-colon URL.
		ImageTag:       ecrTagSuffix(pctx.ImageTag),
		AllowedCIDR:    pctx.CallerIP,
		DesiredCount:   desiredCount(pctx.IsFirstDeploy),
		CPU:            pctx.CPU,
		Memory:         pctx.Memory,
		Port:           pctx.Port,
		LoadBalancer:   pctx.LoadBalancer,
		CertificateARN: pctx.CertificateARN,
		EnvVars:        sortedEnvVars(pctx.EnvVars),
		SecretVars:     sortedSecretARNs(pctx.SecretARNs),
		IsDestroy:      pctx.Destroy,
		Backend:        &backend,
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
// The hclQuote function is registered so templates can safely quote strings.
func renderFile(tmplPath, outPath string, spec TerraformSpec) error {
	funcMap := template.FuncMap{
		// hclQuote wraps s in HCL double-quote syntax with proper escaping.
		// It uses Go strconv.Quote (same escape sequences as HCL) then re-wraps.
		"hclQuote": func(s string) string { return strconv.Quote(s) },
	}
	tmpl, err := template.New(filepath.Base(tmplPath)).Funcs(funcMap).ParseFS(templateFS, tmplPath)
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

// sortedEnvVars converts the EnvVars map to a sorted slice for deterministic
// template output (map iteration order is undefined in Go).
func sortedEnvVars(m map[string]string) []EnvVar {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]EnvVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, EnvVar{Name: k, Value: m[k]})
	}
	return out
}

// sortedSecretARNs converts the SecretARNs map to a sorted SecretVar slice for
// deterministic template output. The container receives each secret as an env
// var sourced directly from Secrets Manager at task startup time.
func sortedSecretARNs(m map[string]string) []SecretVar {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]SecretVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, SecretVar{Name: k, ValueFrom: m[k]})
	}
	return out
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
