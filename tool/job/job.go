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

// Tool manages background jobs started by the bash tool.
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
		Name:        "Job",
		Description: "Manage background jobs",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["status", "kill", "list", "poll"]
    },
    "job_id": {
      "type": "string",
      "description": "job id"
    },
    "wait_seconds": {
      "type": "integer",
      "minimum": 0,
      "description": "For poll: max time to wait for the job to finish. Default 60."
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
	case "status":
		job, ok := t.manager.Status(input.JobID)
		if !ok {
			return t.result(`{"error": "job not found"}`), nil
		}
		return t.result(encodeJob(job)), nil

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
		job, ok := t.manager.Wait(input.JobID, time.Duration(waitSeconds)*time.Second)
		if !ok {
			return t.result(`{"error": "job not found"}`), nil
		}
		stdout, stderr := job.DrainOutput()
		return t.result(fmt.Sprintf(`{"job_id": %q, "status": %q, "exit_code": %d, "stdout": %q, "stderr": %q}`,
			job.ID, job.Status, job.ExitCode, stdout, stderr)), nil

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

func encodeJob(j *job.Job) string {
	return fmt.Sprintf(`{"job_id": %q, "status": %q, "exit_code": %d, "command": %q}`,
		j.ID, j.Status, j.ExitCode, j.Command)
}

var _ dora.Tool = (*Tool)(nil)
