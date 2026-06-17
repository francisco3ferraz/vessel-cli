package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// Compiler implements ports.DockerCompiler.
type Compiler struct{}

// NewCompiler returns a new Compiler.
func NewCompiler() *Compiler { return &Compiler{} }

// Build runs `docker build` and streams every output line as a BuildEvent.
//
// The caller MUST drain the events channel before checking the returned error
// (the channel is closed by the goroutine in orchestrator, not by this method).
// Sets pctx.ImageTag (already set by inspector; passed through) and pctx.ImageID
// on success.
func (c *Compiler) Build(ctx context.Context, pctx *types.PipelineContext, events chan<- ports.BuildEvent) error {
	cmd := exec.CommandContext(ctx, "docker",
		"build",
		"--progress=plain", // structured, parseable text output
		"-f", pctx.DockerfilePath,
		"-t", pctx.ImageTag,
		pctx.ProjectDir,
	)

	stdoutW := newLineWriter(events, ports.BuildEventStep)
	stderrW := newLineWriter(events, ports.BuildEventLog)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	runErr := cmd.Run()

	// Flush any partial line that did not end with a newline.
	stdoutW.flush()
	stderrW.flush()

	if runErr != nil {
		return fmt.Errorf("docker build: %w", runErr)
	}

	// Best-effort: retrieve the full content-addressable image ID.
	if id, err := resolveImageID(ctx, pctx.ImageTag); err == nil {
		pctx.ImageID = id
	}

	events <- ports.BuildEvent{Type: ports.BuildEventDone, Message: pctx.ImageTag}
	return nil
}

// Push is not yet implemented (Phase 4).
// It is present to satisfy the ports.DockerCompiler interface.
func (c *Compiler) Push(_ context.Context, _ *types.PipelineContext, _ chan<- ports.BuildEvent) error {
	return nil // TODO Phase 4: ecr:GetAuthorizationToken → docker login → docker push
}

// resolveImageID returns the full SHA256 image ID for the given tag.
func resolveImageID(ctx context.Context, tag string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "--format={{.Id}}", tag).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
