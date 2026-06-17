// export_test.go exposes internal symbols for package-external tests.
// Only compiled during `go test`.
package docker

import "github.com/francisco3ferraz/vessel-cli/pkg/ports"

// NewLineWriterForTest exposes the unexported lineWriter constructor.
func NewLineWriterForTest(events chan<- ports.BuildEvent, t ports.BuildEventType) *lineWriter {
	return newLineWriter(events, t)
}

// FlushForTest exposes the unexported flush method.
func (w *lineWriter) FlushForTest() {
	w.flush()
}

// ImageTagSuffixForTest exposes imageTagSuffix for unit tests.
func ImageTagSuffixForTest(tag string) string { return imageTagSuffix(tag) }

// ECRRepoNameForTest exposes ecrRepoName for unit tests.
func ECRRepoNameForTest(uri string) string { return ecrRepoName(uri) }
