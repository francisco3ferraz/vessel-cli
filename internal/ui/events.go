// Package ui provides the terminal rendering layer for the deployment pipeline.
// It consumes structured PipelineEvents and renders spinners, progress bars,
// and stage summaries to stderr, leaving stdout clean for structured output.
package ui

// PipelineEvent is a structured event emitted by a pipeline stage.
// Future stages (Docker build, Terraform apply) will stream these via channels
// for real-time output rendering.
type PipelineEvent struct {
	Type    PipelineEventType
	Stage   string // e.g., "Docker Build"
	Message string
	Err     error // non-nil only for EventStageError
}

// PipelineEventType enumerates all event variants the Renderer handles.
type PipelineEventType int

const (
	EventStageStart   PipelineEventType = iota // A named stage has begun
	EventStageLog                              // A progress log line within a stage
	EventStageDone                             // A stage completed successfully
	EventStageError                            // A stage failed
	EventPipelineDone                          // The full pipeline completed
)
