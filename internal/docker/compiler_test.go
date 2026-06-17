package docker_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/francisco3ferraz/vessel-cli/internal/docker"
	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// ─── Compile-time interface check ─────────────────────────────────────────────
// If Compiler stops satisfying DockerCompiler this line fails to compile.
var _ ports.DockerCompiler = &docker.Compiler{}

// ─── lineWriter unit tests ────────────────────────────────────────────────────
// Pure logic tests — no Docker daemon required.

func TestLineWriter_EmitsOneEventPerLine(t *testing.T) {
	events := make(chan ports.BuildEvent, 16)
	w := docker.NewLineWriterForTest(events, ports.BuildEventLog)
	w.Write([]byte("line one\nline two\n"))
	close(events)

	got := collectMessages(events)
	if len(got) != 2 || got[0] != "line one" || got[1] != "line two" {
		t.Errorf("expected [line one, line two], got %v", got)
	}
}

func TestLineWriter_BuffersPartialLines(t *testing.T) {
	events := make(chan ports.BuildEvent, 16)
	w := docker.NewLineWriterForTest(events, ports.BuildEventLog)
	w.Write([]byte("partial"))
	w.Write([]byte(" line\n"))
	close(events)

	got := collectMessages(events)
	if len(got) != 1 || got[0] != "partial line" {
		t.Errorf("expected [partial line], got %v", got)
	}
}

func TestLineWriter_FlushEmitsTrailingContent(t *testing.T) {
	events := make(chan ports.BuildEvent, 16)
	w := docker.NewLineWriterForTest(events, ports.BuildEventLog)
	w.Write([]byte("no newline"))
	w.FlushForTest()
	close(events)

	got := collectMessages(events)
	if len(got) != 1 || got[0] != "no newline" {
		t.Errorf("expected [no newline], got %v", got)
	}
}

func TestLineWriter_SkipsBlankLines(t *testing.T) {
	events := make(chan ports.BuildEvent, 16)
	w := docker.NewLineWriterForTest(events, ports.BuildEventLog)
	w.Write([]byte("real\n\n\nalso real\n"))
	close(events)

	got := collectMessages(events)
	if len(got) != 2 {
		t.Errorf("expected 2 non-blank events, got %d: %v", len(got), got)
	}
}

func TestLineWriter_StripsCR(t *testing.T) {
	events := make(chan ports.BuildEvent, 16)
	w := docker.NewLineWriterForTest(events, ports.BuildEventLog)
	w.Write([]byte("windows line\r\n"))
	close(events)

	got := collectMessages(events)
	if len(got) != 1 || got[0] != "windows line" {
		t.Errorf("expected CR stripped, got %v", got)
	}
}

// ─── Push helper unit tests (no Docker/AWS required) ─────────────────────────

func TestImageTagSuffix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"my-app:abc1234", "abc1234"},
		{"my-app:latest", "latest"},
		{"my-app", "latest"},   // no colon → defaults to latest
		{":only-tag", "only-tag"},
	}
	for _, c := range cases {
		got := docker.ImageTagSuffixForTest(c.in)
		if got != c.want {
			t.Errorf("ImageTagSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestECRRepoName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"123456789.dkr.ecr.us-east-1.amazonaws.com/my-app", "my-app"},
		{"123456789.dkr.ecr.us-east-1.amazonaws.com/org/my-app", "org/my-app"},
		{"just-a-name", "just-a-name"}, // no slash → return as-is
	}
	for _, c := range cases {
		got := docker.ECRRepoNameForTest(c.in)
		if got != c.want {
			t.Errorf("ECRRepoName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ─── Integration test (auto-skipped if Docker is not available) ───────────────

func TestCompiler_DockerUnavailable_Skip(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker daemon not available")
	}
	c := docker.NewCompiler()
	if c == nil {
		t.Fatal("NewCompiler returned nil")
	}
	// Push with empty ECRRepositoryURI must return a clear error, not panic.
	events := make(chan ports.BuildEvent, 4)
	pctx := &types.PipelineContext{AWSRegion: "us-east-1"}
	err := c.Push(context.Background(), pctx, events)
	close(events)
	if err == nil {
		t.Error("expected error from Push with empty ECRRepositoryURI")
	}
	if !strings.Contains(err.Error(), "ECRRepositoryURI") {
		t.Errorf("expected ECRRepositoryURI mention in error, got: %v", err)
	}
}

// ─── Helper ───────────────────────────────────────────────────────────────────

func collectMessages(events <-chan ports.BuildEvent) []string {
	var out []string
	for e := range events {
		out = append(out, e.Message)
	}
	return out
}
