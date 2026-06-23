package terraform

import (
	"encoding/json"
	"fmt"

	"github.com/francisco3ferraz/vessel-cli/pkg/types"
)

// tfOutputValue represents one entry in `terraform output -json` output.
// Schema: {"output_name": {"sensitive": bool, "type": ..., "value": <json>}}
type tfOutputValue struct {
	Sensitive bool            `json:"sensitive"`
	Type      json.RawMessage `json:"type"`
	Value     json.RawMessage `json:"value"`
}

// ParseOutputJSON parses the raw bytes from `terraform output -json`
// into a typed CloudOutputs struct.
//
// Unknown or missing output keys are silently treated as empty strings —
// the executor will catch missing required values at the state-save step.
func ParseOutputJSON(data []byte) (*types.CloudOutputs, error) {
	var raw map[string]tfOutputValue
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse terraform output JSON: %w", err)
	}

	get := func(key string) string {
		v, ok := raw[key]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(v.Value, &s); err != nil {
			return ""
		}
		return s
	}

	return &types.CloudOutputs{
		ECRRepositoryURI: get("ecr_repository_uri"),
		ECSClusterARN:    get("ecs_cluster_arn"),
		ECSServiceARN:    get("ecs_service_arn"),
		ECSTaskDefARN:    get("ecs_task_def_arn"),
		ALBDNSName:       get("alb_dns_name"),
		ALBARN:           get("alb_arn"),
	}, nil
}
