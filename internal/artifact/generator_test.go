package artifact_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/francisco3ferraz/vessel-cli/internal/artifact"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// mkProject creates a minimal fake Go project in a temp dir and returns its path.
// It creates .vessel-cli/ (workspace manager would normally do this) so the
// generator can write Dockerfile there without needing the full workspace stack.
func mkProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".vessel-cli"), 0755)
	return dir
}

func pctx(dir, goVer, binary string) *types.PipelineContext {
	return &types.PipelineContext{
		ProjectDir: dir,
		GoVersion:  goVer,
		BinaryName: binary,
	}
}

// ─── Dockerfile rendering ─────────────────────────────────────────────────────

func TestGenerateDockerfile_RendersGoVersionAndBinary(t *testing.T) {
	dir := mkProject(t)
	p := pctx(dir, "1.22", "my-service")

	if err := artifact.NewGenerator().GenerateDockerfile(context.Background(), p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(p.DockerfilePath)
	if err != nil {
		t.Fatalf("Dockerfile not created at %s: %v", p.DockerfilePath, err)
	}
	content := string(data)

	checks := []struct {
		label string
		want  string
	}{
		{"go version", "golang:1.22-alpine"},
		{"binary copy", "/my-service"},
		{"entrypoint", `ENTRYPOINT ["/my-service"]`},
		{"port", "EXPOSE 8080"},
		{"distroless", "distroless/static"},
		{"platform", "--platform=linux/amd64"},
		{"cgo disabled", "CGO_ENABLED=0"},
	}
	for _, c := range checks {
		if !strings.Contains(content, c.want) {
			t.Errorf("[%s] expected %q in Dockerfile:\n%s", c.label, c.want, content)
		}
	}
}

func TestGenerateDockerfile_SetsDockerfilePath(t *testing.T) {
	dir := mkProject(t)
	p := pctx(dir, "1.22", "svc")
	artifact.NewGenerator().GenerateDockerfile(context.Background(), p)

	want := filepath.Join(dir, ".vessel-cli", "Dockerfile")
	if p.DockerfilePath != want {
		t.Errorf("expected DockerfilePath=%s, got %s", want, p.DockerfilePath)
	}
}

func TestGenerateDockerfile_Idempotent(t *testing.T) {
	dir := mkProject(t)
	p := pctx(dir, "1.22", "svc")
	g := artifact.NewGenerator()

	if err := g.GenerateDockerfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(p.DockerfilePath)

	if err := g.GenerateDockerfile(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(p.DockerfilePath)

	if string(first) != string(second) {
		t.Error("second run produced a different Dockerfile — generator is not idempotent")
	}
}

// ─── Build target detection ───────────────────────────────────────────────────

func TestGenerateDockerfile_BuildTarget_RootMain(t *testing.T) {
	dir := mkProject(t)
	// No cmd/ directory — build target should be "."
	p := pctx(dir, "1.22", "svc")
	artifact.NewGenerator().GenerateDockerfile(context.Background(), p)

	data, _ := os.ReadFile(p.DockerfilePath)
	if !strings.Contains(string(data), "go build") {
		t.Fatal("expected go build in Dockerfile")
	}
	if !strings.Contains(string(data), " .") {
		t.Errorf("expected build target '.' for root-main project; Dockerfile:\n%s", data)
	}
}

func TestGenerateDockerfile_BuildTarget_SpecificCmdDir(t *testing.T) {
	dir := mkProject(t)
	os.MkdirAll(filepath.Join(dir, "cmd", "my-service"), 0755)
	p := pctx(dir, "1.22", "my-service")
	artifact.NewGenerator().GenerateDockerfile(context.Background(), p)

	data, _ := os.ReadFile(p.DockerfilePath)
	if !strings.Contains(string(data), "./cmd/my-service") {
		t.Errorf("expected ./cmd/my-service build target; Dockerfile:\n%s", data)
	}
}

// ─── .dockerignore handling ───────────────────────────────────────────────────

func TestDockerignore_CreatedWhenAbsent(t *testing.T) {
	dir := mkProject(t)
	p := pctx(dir, "1.22", "svc")
	artifact.NewGenerator().GenerateDockerfile(context.Background(), p)

	data, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	if err != nil {
		t.Fatalf(".dockerignore not created: %v", err)
	}
	if !strings.Contains(string(data), ".vessel-cli/") {
		t.Error(".dockerignore should exclude .vessel-cli/")
	}
}

func TestDockerignore_NotOverwrittenWhenPresent(t *testing.T) {
	dir := mkProject(t)
	diPath := filepath.Join(dir, ".dockerignore")
	existing := "node_modules/\ndist/\n"
	os.WriteFile(diPath, []byte(existing), 0644)

	p := pctx(dir, "1.22", "svc")
	artifact.NewGenerator().GenerateDockerfile(context.Background(), p)

	data, _ := os.ReadFile(diPath)
	if string(data) != existing {
		t.Errorf("existing .dockerignore was overwritten\nwant: %q\ngot:  %q", existing, string(data))
	}
}
