package artifact

// DockerfileSpec holds the data projected into the Dockerfile template.
// It is derived from PipelineContext and is the only input to the renderer.
type DockerfileSpec struct {
	GoVersion   string // e.g., "1.22" — used in FROM golang:{{.GoVersion}}-alpine
	BinaryName  string // e.g., "my-service" — binary name and ENTRYPOINT
	BuildTarget string // e.g., "." or "./cmd/my-service" — passed to go build
	Port        int    // container port to EXPOSE; always 8080 in v1
}
