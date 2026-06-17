// Package pipeline owns the orchestration layer of the deployment pipeline.
// It sequences the five stages and owns the PipelineContext as it flows through them.
//
// PipelineContext and CloudOutputs are defined in pkg/types (the shared data leaf).
// They are re-exported here as type aliases so callers that import
// internal/pipeline directly (e.g. cmd/deploy.go) do not need to change.
package pipeline

import "github.com/francisco3ferraz/vessel-cli/pkg/types"

// PipelineContext re-exported from pkg/types.
type PipelineContext = types.PipelineContext

// CloudOutputs re-exported from pkg/types.
type CloudOutputs = types.CloudOutputs
