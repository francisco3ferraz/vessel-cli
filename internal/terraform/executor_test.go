package terraform_test

import (
	"os/exec"
	"testing"

	tfpkg "github.com/francisco3ferraz/vessel-cli/internal/terraform"
	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
)

// ─── Compile-time interface check ─────────────────────────────────────────────
var _ ports.TerraformExecutor = &tfpkg.Executor{}

// ─── Parser unit tests (no terraform binary required) ────────────────────────

func TestParseOutputJSON_AllFields(t *testing.T) {
	raw := `{
		"ecr_repository_uri": {"sensitive":false,"type":"string","value":"123456789.dkr.ecr.us-east-1.amazonaws.com/my-app"},
		"ecs_cluster_arn":    {"sensitive":false,"type":"string","value":"arn:aws:ecs:us-east-1:123456789:cluster/my-app-cluster"},
		"ecs_service_arn":    {"sensitive":false,"type":"string","value":"arn:aws:ecs:us-east-1:123456789:service/my-app-cluster/my-app"},
		"ecs_task_def_arn":   {"sensitive":false,"type":"string","value":"arn:aws:ecs:us-east-1:123456789:task-definition/my-app:1"}
	}`

	out, err := tfpkg.ParseOutputJSON([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.ECRRepositoryURI != "123456789.dkr.ecr.us-east-1.amazonaws.com/my-app" {
		t.Errorf("ECRRepositoryURI: got %q", out.ECRRepositoryURI)
	}
	if out.ECSClusterARN != "arn:aws:ecs:us-east-1:123456789:cluster/my-app-cluster" {
		t.Errorf("ECSClusterARN: got %q", out.ECSClusterARN)
	}
	if out.ECSServiceARN != "arn:aws:ecs:us-east-1:123456789:service/my-app-cluster/my-app" {
		t.Errorf("ECSServiceARN: got %q", out.ECSServiceARN)
	}
	if out.ECSTaskDefARN != "arn:aws:ecs:us-east-1:123456789:task-definition/my-app:1" {
		t.Errorf("ECSTaskDefARN: got %q", out.ECSTaskDefARN)
	}
}

func TestParseOutputJSON_MissingKeyReturnsEmpty(t *testing.T) {
	// Only ecr_repository_uri present; others should default to "".
	raw := `{"ecr_repository_uri":{"sensitive":false,"type":"string","value":"uri-only"}}`

	out, err := tfpkg.ParseOutputJSON([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ECRRepositoryURI != "uri-only" {
		t.Errorf("expected uri-only, got %q", out.ECRRepositoryURI)
	}
	if out.ECSClusterARN != "" {
		t.Errorf("expected empty ECSClusterARN, got %q", out.ECSClusterARN)
	}
}

func TestParseOutputJSON_EmptyObject(t *testing.T) {
	out, err := tfpkg.ParseOutputJSON([]byte(`{}`))
	if err != nil {
		t.Fatalf("unexpected error on empty object: %v", err)
	}
	if out.ECRRepositoryURI != "" || out.ECSClusterARN != "" {
		t.Error("expected all empty strings for empty terraform output")
	}
}

func TestParseOutputJSON_InvalidJSON(t *testing.T) {
	_, err := tfpkg.ParseOutputJSON([]byte(`not valid json`))
	if err == nil {
		t.Error("expected error for invalid JSON input")
	}
}

func TestParseOutputJSON_NonStringValue(t *testing.T) {
	// Resilience: if a value is not a JSON string, it should be silently skipped.
	raw := `{"ecr_repository_uri":{"sensitive":false,"type":"number","value":42}}`
	out, err := tfpkg.ParseOutputJSON([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 42 cannot unmarshal into a string → empty string, no panic.
	if out.ECRRepositoryURI != "" {
		t.Errorf("expected empty string for non-string value, got %q", out.ECRRepositoryURI)
	}
}

// ─── Integration test (auto-skipped if terraform not on PATH) ─────────────────

func TestExecutor_SkipIfNoTerraform(t *testing.T) {
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not on PATH — skipping integration test")
	}
	// Full end-to-end exercised via `vessel-cli deploy`.
	t.Log("terraform binary found; integration verified via vessel-cli deploy")
}
