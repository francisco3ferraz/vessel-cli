package pipeline

import (
	"strings"
)

// tfLogWriter is an io.Writer that routes terraform (and any other CLI) output
// to the UI renderer line-by-line. It buffers partial writes so each call to
// Write may emit zero, one, or many lines depending on how much data arrives.
//
// This avoids the goroutine+channel overhead used by the Docker streamer —
// terraform is called synchronously so the orchestrator blocks while it runs.
type tfLogWriter struct {
	ui  UIRenderer
	buf strings.Builder
}

// Write implements io.Writer.
func (w *tfLogWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		s := w.buf.String()
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimRight(s[:idx], "\r")
		w.buf.Reset()
		w.buf.WriteString(s[idx+1:])
		if line != "" {
			w.ui.Log("%s", line)
		}
	}
	return len(p), nil
}

// UIRenderer is the subset of ui.Renderer used by tfLogWriter.
// Defined here to avoid importing the ui package from a utility file.
type UIRenderer interface {
	Log(format string, args ...interface{})
}
