package ui

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/briandowns/spinner"
)

// Renderer provides a terminal UI for the deployment pipeline.
// It wraps a spinner for in-progress stages and prints structured
// completion/failure messages. Writes to stderr by default, leaving
// stdout clean for structured output.
type Renderer struct {
	spin    *spinner.Spinner
	out     io.Writer
	running bool
}

// New creates a Renderer that writes to out.
func New(out io.Writer) *Renderer {
	s := spinner.New(spinner.CharSets[14], 80*time.Millisecond, spinner.WithWriter(out))
	_ = s.Color("cyan", "bold")
	return &Renderer{spin: s, out: out}
}

// NewDefault returns a Renderer that writes to os.Stderr.
func NewDefault() *Renderer { return New(os.Stderr) }

// StartStage begins a named stage with a spinning indicator.
func (r *Renderer) StartStage(name, desc string) {
	r.spin.Suffix = fmt.Sprintf("  %s: %s", name, desc)
	r.spin.Start()
	r.running = true
}

// CompleteStage stops the spinner and prints a success line.
func (r *Renderer) CompleteStage(msg string) {
	r.spin.Stop()
	r.running = false
	fmt.Fprintf(r.out, "  ✓ %s\n", msg)
}

// FailStage stops the spinner and prints a failure line.
func (r *Renderer) FailStage(err error) {
	r.spin.Stop()
	r.running = false
	fmt.Fprintf(r.out, "  ✗ %s\n", err)
}

// Log prints a log line beneath the current stage, pausing the spinner
// briefly to avoid overwriting the line.
func (r *Renderer) Log(format string, args ...interface{}) {
	if r.running {
		r.spin.Stop()
		fmt.Fprintf(r.out, "    → "+format+"\n", args...)
		r.spin.Start()
	} else {
		fmt.Fprintf(r.out, "    → "+format+"\n", args...)
	}
}

// Printf writes a formatted line without affecting the spinner.
func (r *Renderer) Printf(format string, args ...interface{}) {
	if r.running {
		r.spin.Stop()
		fmt.Fprintf(r.out, format+"\n", args...)
		r.spin.Start()
	} else {
		fmt.Fprintf(r.out, format+"\n", args...)
	}
}
