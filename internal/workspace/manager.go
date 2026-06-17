// Package workspace implements workspace management, project inspection,
// state persistence, and preflight checks for vessel-cli.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// WorkspaceManager manages the .vessel-cli/ directory lifecycle.
// It creates the workspace on first run, injects the .gitignore entry,
// and holds the exclusive file lock that prevents concurrent deployments.
//
// WorkspaceManager satisfies ports.WorkspaceInitializer.
type WorkspaceManager struct {
	projectDir string
	lockFd     *os.File
}

// WorkspacePaths holds all canonical paths derived from the project directory.
type WorkspacePaths struct {
	Base      string // .vessel-cli/
	StateFile string // .vessel-cli/state.json
	TFDir     string // .vessel-cli/tf/
	LockFile  string // .vessel-cli/.lock
}

// NewManager creates a WorkspaceManager for the given project root.
func NewManager(projectDir string) *WorkspaceManager {
	return &WorkspaceManager{projectDir: projectDir}
}

// Paths returns all canonical workspace paths derived from projectDir.
func (m *WorkspaceManager) Paths() WorkspacePaths {
	base := filepath.Join(m.projectDir, ".vessel-cli")
	return WorkspacePaths{
		Base:      base,
		StateFile: filepath.Join(base, "state.json"),
		TFDir:     filepath.Join(base, "tf"),
		LockFile:  filepath.Join(base, ".lock"),
	}
}

// Init creates the workspace directory structure and injects the .gitignore
// entry. Idempotent: safe to call on every run.
func (m *WorkspaceManager) Init() error {
	paths := m.Paths()
	if err := os.MkdirAll(paths.TFDir, 0755); err != nil {
		return fmt.Errorf("create workspace %s: %w", paths.TFDir, err)
	}
	if err := ensureGitignore(m.projectDir); err != nil {
		// Non-fatal: warn but do not block the deployment.
		fmt.Fprintf(os.Stderr, "warning: could not update .gitignore: %v\n", err)
	}
	return nil
}

// AcquireLock acquires an exclusive advisory lock on .vessel-cli/.lock.
// Returns immediately (non-blocking) if another process holds the lock.
// The caller MUST call ReleaseLock when done.
func (m *WorkspaceManager) AcquireLock() error {
	paths := m.Paths()
	f, err := os.OpenFile(paths.LockFile, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open lock file %s: %w", paths.LockFile, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return fmt.Errorf(
			"another vessel-cli deployment is already in progress\n"+
				"  (lock held at %s)\n"+
				"  If no other deploy is running, delete the lock file and retry",
			paths.LockFile,
		)
	}
	m.lockFd = f
	return nil
}

// ReleaseLock releases the workspace lock. Safe to call if no lock is held.
func (m *WorkspaceManager) ReleaseLock() {
	if m.lockFd != nil {
		_ = syscall.Flock(int(m.lockFd.Fd()), syscall.LOCK_UN)
		_ = m.lockFd.Close()
		m.lockFd = nil
	}
}

// ensureGitignore appends the .vessel-cli/ entry to .gitignore if not present.
func ensureGitignore(projectDir string) error {
	const entry = ".vessel-cli/"
	gitignorePath := filepath.Join(projectDir, ".gitignore")

	content, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(content), entry) {
		return nil // Already present.
	}

	f, err := os.OpenFile(gitignorePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\n# vessel-cli managed workspace\n%s\n", entry)
	return err
}
