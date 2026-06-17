package docker_test

import (
	"context"
	"os/exec"
	"testing"

	"github.com/francisco3ferraz/vessel-cli/internal/docker"
	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
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

// ─── Integration test (auto-skipped if Docker is not available) ───────────────

func TestCompiler_DockerUnavailable_Skip(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker daemon not available")
	}
	// If Docker IS available, we just assert the compiler was created — the
	// real build is exercised manually via `vessel-cli deploy`.
	c := docker.NewCompiler()
	if c == nil {
		t.Fatal("NewCompiler returned nil")
	}
	// Assert Push stub returns nil (Phase 4 stub contract).
	events := make(chan ports.BuildEvent, 1)
	if err := c.Push(context.Background(), nil, events); err != nil {
		t.Errorf("Push stub should return nil, got: %v", err)
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
