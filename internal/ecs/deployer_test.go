package ecs_test

import (
	"context"
	"testing"

	ecspkg "github.com/francisco3ferraz/vessel-cli/internal/ecs"
	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
)

// ─── Compile-time interface check ─────────────────────────────────────────────
var _ ports.Deployer = &ecspkg.Deployer{}

// ─── Unit tests ───────────────────────────────────────────────────────────────

func TestNewDeployer_NotNil(t *testing.T) {
	d := ecspkg.NewDeployer("us-east-1", "")
	if d == nil {
		t.Fatal("NewDeployer returned nil")
	}
}

func TestNewDeployer_WithProfile(t *testing.T) {
	d := ecspkg.NewDeployer("eu-west-1", "staging")
	if d == nil {
		t.Fatal("NewDeployer returned nil with profile")
	}
}

func TestDeployer_Scale_FailsWithoutAWS(t *testing.T) {
	// Without real AWS creds pointing at a real cluster, Scale must return
	// an error — not panic or hang. We verify the failure mode is clean.
	//
	// This test runs quickly because the AWS SDK config load fails fast
	// when the region is invalid or creds are missing.
	d := ecspkg.NewDeployer("us-east-1", "vessel-cli-nonexistent-profile-xyz")
	err := d.Scale(context.Background(),
		"arn:aws:ecs:us-east-1:000000000000:cluster/does-not-exist",
		"arn:aws:ecs:us-east-1:000000000000:service/does-not-exist/does-not-exist",
		1,
	)
	if err == nil {
		t.Error("expected error from Scale with non-existent profile/cluster, got nil")
	}
	t.Logf("Scale returned expected error: %v", err)
}

// ─── Integration test (auto-skipped without real AWS env) ─────────────────────

func TestDeployer_Scale_Integration(t *testing.T) {
	// This test is intentionally left as a manual / CI integration test.
	// Real verification happens via `vessel-cli deploy` against a live cluster.
	t.Skip("integration test: run via vessel-cli deploy")
}
