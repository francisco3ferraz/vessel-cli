// export_test.go exposes internal symbols needed by package-external tests.
// This file is only compiled during `go test` (package name without _test suffix
// but in a *_test.go file is the pattern Go uses for white-box test exports).
package docker

import "github.com/francisco3ferraz/vessel-cli/pkg/ports"

// NewLineWriterForTest exposes the unexported lineWriter constructor for tests.
func NewLineWriterForTest(events chan<- ports.BuildEvent, t ports.BuildEventType) *lineWriter {
	return newLineWriter(events, t)
}

// FlushForTest exposes the unexported flush method for tests.
func (w *lineWriter) FlushForTest() {
	w.flush()
}
