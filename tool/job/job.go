// Package jobtool implements a dora.Tool for managing background jobs.
package jobtool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/job"
)

const defaultPollSeconds = 60

// Tool manages background command and Agent Task jobs.
type Tool struct {
	manager *job.Manager
}

// New creates a Job tool backed by the given manager.
func New(manager *job.Manager) *Tool {
	return &Tool{manager: manager}
}

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "job",
		Description: "Manage background jobs started by bash, powershell, or task (kill, list, poll). Poll with wait_seconds:0 returns a non-blocking status snapshot.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["kill", "list", "poll"]
    },
    "job_id": {
      "type": "string",
      "description": "job id"
    },
    "wait_seconds": {
      "type": "integer",
      "minimum": 0,
      "description": "For poll: max time to wait for the job to finish. Use 0 for a non-blocking status snapshot that returns immediately. Default 60."
    }
  },
  "required": ["action"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	if t == nil || t.manager == nil {
		return dora.ToolResult{}, errors.New("job: tool is not initialized")
	}
	input, err := decodeInput(raw)
	if err != nil {
		return dora.ToolResult{}, err
	}

	switch input.Action {
	case "kill":
		if err := t.manager.Kill(input.JobID); err != nil {
			return t.result(fmt.Sprintf(`{"error": %q}`, err.Error())), nil
		}
		return t.result(fmt.Sprintf(`{"job_id": %q, "status": "killed"}`, input.JobID)), nil

	case "list":
		jobs := t.manager.List()
		type jobInfo struct {
			ID     string `json:"job_id"`
			Status string `json:"status"`
		}
		infos := make([]jobInfo, 0, len(jobs))
		for _, j := range jobs {
			infos = append(infos, jobInfo{ID: j.ID, Status: string(j.Status)})
		}
		data, _ := json.Marshal(infos)
		return t.result(fmt.Sprintf(`{"jobs": %s}`, data)), nil

	case "poll":
		waitSeconds := defaultPollSeconds
		if input.WaitSeconds != nil {
			waitSeconds = *input.WaitSeconds
		}
		snapshot, ok := t.manager.Poll(input.JobID, time.Duration(waitSeconds)*time.Second)
		if !ok {
			return t.result(`{"error": "job not found"}`), nil
		}
		if snapshot.Kind == job.KindTask {
			type taskResult struct {
				JobID  string `json:"job_id"`
				Status string `json:"status"`
				Result string `json:"result,omitempty"`
				Error  string `json:"error,omitempty"`
			}
			data, _ := json.Marshal(taskResult{
				JobID:  snapshot.ID,
				Status: string(snapshot.Status),
				Result: snapshot.Result,
				Error:  snapshot.Error,
			})
			return t.result(string(data)), nil
		}
		return t.result(fmt.Sprintf(`{"job_id": %q, "status": %q, "exit_code": %d, "stdout": %q, "stderr": %q}`,
			snapshot.ID, snapshot.Status, snapshot.ExitCode, snapshot.Stdout, snapshot.Stderr)), nil

	default:
		return dora.ToolResult{}, fmt.Errorf("job: unknown action %q", input.Action)
	}
}

func (t *Tool) result(content string) dora.ToolResult {
	return dora.ToolResult{Content: content}
}

type input struct {
	Action      string `json:"action"`
	JobID       string `json:"job_id"`
	WaitSeconds *int   `json:"wait_seconds,omitempty"`
}

func decodeInput(raw json.RawMessage) (input, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value input
	if err := decoder.Decode(&value); err != nil {
		return input{}, fmt.Errorf("job: decode input: %w", err)
	}
	if value.Action == "" {
		return input{}, errors.New("job: action is required")
	}
	if value.Action != "list" && value.JobID == "" {
		return input{}, errors.New("job: job_id is required")
	}
	if value.WaitSeconds != nil && *value.WaitSeconds < 0 {
		return input{}, errors.New("job: wait_seconds must be non-negative")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return input{}, errors.New("job: input must contain one JSON value")
		}
		return input{}, fmt.Errorf("job: decode input: %w", err)
	}
	return value, nil
}

var _ dora.Tool = (*Tool)(nil)
