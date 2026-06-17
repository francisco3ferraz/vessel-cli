// Package docker implements the DockerCompiler port using the docker CLI.
// The Docker SDK (github.com/docker/docker) is intentionally avoided in v1:
// its +incompatible module has a Linux compile issue with sockets.DialPipe
// in docker/go-connections. The CLI produces identical output, requires no
// extra dependencies, and is available wherever Docker is installed.
package docker

import (
	"strings"

	"github.com/francisco3ferraz/vessel-cli/pkg/ports"
)

// lineWriter implements io.Writer. It buffers partial writes and emits a
// BuildEvent for each complete newline-terminated line. Partial content
// remaining in the buffer after the last write can be flushed explicitly.
type lineWriter struct {
	events    chan<- ports.BuildEvent
	eventType ports.BuildEventType
	buf       strings.Builder
}

func newLineWriter(events chan<- ports.BuildEvent, t ports.BuildEventType) *lineWriter {
	return &lineWriter{events: events, eventType: t}
}

// Write implements io.Writer. Emits one BuildEvent per newline.
func (w *lineWriter) Write(p []byte) (int, error) {
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
			w.events <- ports.BuildEvent{Type: w.eventType, Message: line}
		}
	}
	return len(p), nil
}

// flush emits any content remaining in the buffer as a final event.
func (w *lineWriter) flush() {
	if s := strings.TrimSpace(w.buf.String()); s != "" {
		w.events <- ports.BuildEvent{Type: w.eventType, Message: s}
		w.buf.Reset()
	}
}
