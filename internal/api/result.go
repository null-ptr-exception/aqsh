package api

import (
	"encoding/json"

	"github.com/rophy/aqsh/internal/worker"
)

const maxResultJSONBytes = 1024 * 1024

type executionResult struct {
	worker.TaskResult
	JSON        json.RawMessage `json:"json,omitempty"`
	JSONOmitted string          `json:"json_omitted,omitempty"`
}

// Preserve data's string contract; callers explicitly opt into an additional
// parsed value. Invalid or oversized data is never truncated into partial JSON.
func structuredResult(result worker.TaskResult) executionResult {
	out := executionResult{TaskResult: result}
	switch {
	case result.Data == nil:
		out.JSONOmitted = "absent"
	case *result.Data == "":
		out.JSONOmitted = "empty"
	case len(*result.Data) > maxResultJSONBytes:
		out.JSONOmitted = "too_large"
	case !json.Valid([]byte(*result.Data)):
		out.JSONOmitted = "invalid_json"
	default:
		out.JSON = json.RawMessage(*result.Data)
	}
	return out
}
