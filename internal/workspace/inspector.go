package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/francisco3ferraz/vessel-cli/internal/pipeline"
)

// Inspector implements ports.WorkspaceInspector.
// It validates that the project directory is a Go project and extracts metadata.
type Inspector struct{}

// NewInspector returns a new Inspector.
func NewInspector() *Inspector { return &Inspector{} }

// Inspect validates the project directory and populates pipeline context fields.
//
// Fails if:
//   - go.mod is missing or unparseable
//   - No main package (main.go or cmd/ directory) is found
//   - Working tree is dirty and pctx.TagOverride is not set (Q3)
func (i *Inspector) Inspect(_ context.Context, pctx *pipeline.PipelineContext) error {
	// 1. Find and parse go.mod.
	goModPath := filepath.Join(pctx.ProjectDir, "go.mod")
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return fmt.Errorf(
			"no go.mod found in %s: not a Go project\n"+
				"  Run: go mod init <module-name>",
			pctx.ProjectDir,
		)
	}

	f, err := modfile.Parse(goModPath, data, nil)
	if err != nil {
		return fmt.Errorf("invalid go.mod: %w", err)
	}
	if f.Module == nil {
		return fmt.Errorf("go.mod has no module declaration")
	}

	pctx.ModuleName = f.Module.Mod.Path
	if f.Go != nil {
		pctx.GoVersion = f.Go.Version
	} else {
		pctx.GoVersion = "1.22" // safe default if directive is absent
	}

	// Derive binary name from the last path segment of the module name.
	parts := strings.Split(pctx.ModuleName, "/")
	pctx.BinaryName = parts[len(parts)-1]

	// 2. Validate a main package exists.
	if err := validateMainPackage(pctx.ProjectDir); err != nil {
		return err
	}

	// 3. Git state check and image tag resolution (Q3).
	//    --tag bypasses git entirely; dirty tree without --tag is a hard error.
	if pctx.TagOverride != "" {
		pctx.ImageTag = pctx.TagOverride
	} else {
		if err := resolveGitTag(pctx); err != nil {
			return err
		}
	}

	return nil
}

// validateMainPackage returns an error if the project has neither main.go
// at the root nor a cmd/ directory.
func validateMainPackage(projectDir string) error {
	_, errMain := os.Stat(filepath.Join(projectDir, "main.go"))
	_, errCmd := os.Stat(filepath.Join(projectDir, "cmd"))
	if errMain != nil && errCmd != nil {
		return fmt.Errorf(
			"no main.go or cmd/ directory found in %s\n"+
				"  vessel-cli deploys runnable Go programs (package main)",
			projectDir,
		)
	}
	return nil
}

// resolveGitTag checks for uncommitted changes and sets pctx.ImageTag from
// the current commit SHA[:8]. Returns an actionable error if the tree is dirty.
func resolveGitTag(pctx *pipeline.PipelineContext) error {
	shaOut, err := exec.Command("git", "-C", pctx.ProjectDir, "rev-parse", "--short=8", "HEAD").Output()
	if err != nil {
		return fmt.Errorf(
			"not a git repository or git not found: %w\n"+
				"  Run: git init && git add . && git commit -m 'initial commit'\n"+
				"  Or use --tag to provide an explicit image tag",
			err,
		)
	}
	sha := strings.TrimSpace(string(shaOut))

	// Q3: dirty tree = hard error — the image tag must be a stable, reproducible
	// identifier so the ecr:DescribeImages skip-push check is meaningful.
	statusOut, err := exec.Command("git", "-C", pctx.ProjectDir, "status", "--porcelain").Output()
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		return fmt.Errorf(
			"working tree has uncommitted changes\n"+
				"  The image tag is the git SHA — it must be stable and reproducible.\n"+
				"  Options:\n"+
				"    1. Commit: git add . && git commit -m '...'\n"+
				"    2. Override: vessel-cli deploy --tag %s:local",
			pctx.BinaryName,
		)
	}

	pctx.ImageTag = pctx.BinaryName + ":" + sha
	return nil
}
