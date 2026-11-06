// Package commandexec provides the shared execution core for command tools.
package commandexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/job"
)

// defaultWaitSeconds is applied when the caller omits wait_seconds.
var defaultWaitSeconds = 10

// Config describes a command tool and controls its execution.
type Config struct {
	Name        string
	Description string
	Binary      string
	CommandArgs func(string) []string
	// JobManager is required and enables background execution: a command that
	// does not finish within wait_seconds is adopted as a background job and a
	// job_id is returned.
	JobManager *job.Manager
}

// Tool executes commands using a configured executable.
type Tool struct {
	name        string
	description string
	binary      string
	commandArgs func(string) []string
	jobManager  *job.Manager
}

// New creates a command execution tool.
func New(cfg Config) (*Tool, error) {
	if cfg.JobManager == nil {
		return nil, fmt.Errorf("%s: job manager is required", cfg.Name)
	}
	return &Tool{
		name:        cfg.Name,
		description: cfg.Description,
		binary:      cfg.Binary,
		commandArgs: cfg.CommandArgs,
		jobManager:  cfg.JobManager,
	}, nil
}

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        t.name,
		Description: t.description + " Automatically move long-running commands to the background after wait_seconds and return a job_id; the command is not terminated. Use the job tool to manage background jobs.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "command"
    },
    "wait_seconds": {
      "type": "integer",
      "minimum": 0,
      "description": "Seconds to wait for completion before moving the command to the background. 0 moves it to the background immediately. Omitted (or negative) uses the default of 10. Default 10"
    }
  },
  "required": ["command"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool. Command failures are returned as structured
// output so the model can inspect stderr and decide how to proceed.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	input, err := decodeInput(t.name, raw)
	if err != nil {
		return dora.ToolResult{}, err
	}

	// Every command runs through executeWithBackground (the only execution
	// path); there is no pure-foreground branch. wait_seconds selects how long
	// to wait before adopting the command as a background job: omitted or
	// negative uses the default (10), 0 adopts immediately, and a positive
	// value waits that many seconds before adopting.
	waitSeconds := defaultWaitSeconds
	if input.WaitSeconds != nil {
		waitSeconds = *input.WaitSeconds
	}
	if waitSeconds < 0 {
		waitSeconds = defaultWaitSeconds
	}
	return t.executeWithBackground(ctx, input, waitSeconds)
}

// killGrace bounds how long a canceled foreground command is awaited after its
// process tree has been killed. Killing is immediate in practice (and
// cmd.WaitDelay releases pipes held by orphaned descendants), so this only
// protects cancellation from hanging on a process that cannot be reaped.
const killGrace = 3 * time.Second

// executeWithBackground waits up to wait_seconds for the command to finish. If
// it finishes in time, the result is returned. If not, the running process is
// adopted as a background job (not restarted) and a job_id is returned.
//
// The process runs on its own context so that adoption can outlive the calling
// tool run. Until it is adopted, though, nothing else owns it, so the caller's
// cancellation is honored during the foreground wait: the whole process tree is
// killed and the output captured so far is returned with canceled set. A request
// that is already canceled never starts a process.
func (t *Tool) executeWithBackground(ctx context.Context, input input, waitSeconds int) (dora.ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return dora.ToolResult{}, fmt.Errorf("%s: command not started: %w", t.name, err)
	}
	waitDuration := time.Duration(waitSeconds) * time.Second

	// Use an independent context for the process so the foreground wait
	// timeout does not kill it when transitioning to background.
	procCtx, procCancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, t.binary, t.commandArgs(input.Command)...)
	cmd.Dir = dora.WorkingDirectory(ctx)
	configureProcessTree(cmd)
	out := &job.OutputBuffer{}
	cmd.Stdout = out.StdoutWriter()
	cmd.Stderr = out.StderrWriter()
	cmd.WaitDelay = time.Second

	// Start the process. Wait() is called either in the foreground completion
	// path or in the Adopt goroutine (never both).
	if err := cmd.Start(); err != nil {
		procCancel()
		return dora.ToolResult{}, fmt.Errorf("%s: start command: %w", t.name, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	wait := time.NewTimer(waitDuration)
	defer wait.Stop()
	select {
	case runErr := <-done:
		procCancel()
		stdout, stderr := out.Drain()
		result := commandResult{
			Stdout: job.CapOutput(stdout),
			Stderr: job.CapOutput(stderr),
		}
		switch {
		case runErr == nil, errors.Is(runErr, exec.ErrWaitDelay):
			// ErrWaitDelay means the process exited successfully but an
			// orphaned child (for example `server &`) still holds the output
			// pipes; the command itself succeeded.
			result.ExitCode = 0
		default:
			var exitError *exec.ExitError
			if !errors.As(runErr, &exitError) {
				return dora.ToolResult{}, fmt.Errorf("%s: execute command: %w", t.name, runErr)
			}
			result.ExitCode = exitError.ExitCode()
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return dora.ToolResult{}, fmt.Errorf("%s: encode result: %w", t.name, err)
		}
		return t.result(string(encoded)), nil

	case <-ctx.Done():
		return t.cancelForeground(procCancel, done, out)

	case <-wait.C:
		// Cancellation that arrives together with the wait timeout wins: a
		// canceled run must not leave a new background process behind.
		if ctx.Err() != nil {
			return t.cancelForeground(procCancel, done, out)
		}
		// Transition to background: adopt the running process, do not restart.
		// The Wait() goroutine above fills `done`; Adopt reaps it.
		j := t.jobManager.AdoptCommand(t.name, cmd, procCancel, input.Command, out, done)
		stdout, stderr := out.Drain()
		encoded, err := json.Marshal(struct {
			JobID   string `json:"job_id"`
			Status  string `json:"status"`
			Stdout  string `json:"stdout"`
			Stderr  string `json:"stderr"`
			Message string `json:"message"`
		}{
			JobID:   j.ID,
			Status:  "running",
			Stdout:  job.CapOutput(stdout),
			Stderr:  job.CapOutput(stderr),
			Message: fmt.Sprintf("Command did not finish within %s. It is now running in the background. Use the job tool to check status.", waitDuration),
		})
		if err != nil {
			return dora.ToolResult{}, fmt.Errorf("%s: encode result: %w", t.name, err)
		}
		return t.result(string(encoded)), nil
	}
}

// cancelForeground stops a command that was canceled before being adopted: it
// kills the whole process tree, waits a bounded time for the process to be
// reaped, and reports the output captured so far. The result is a normal tool
// result with canceled set rather than an error, so the partial output is kept
// in the saved transcript.
func (t *Tool) cancelForeground(procCancel context.CancelFunc, done <-chan error, out *job.OutputBuffer) (dora.ToolResult, error) {
	procCancel()
	grace := time.NewTimer(killGrace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
	}
	stdout, stderr := out.Drain()
	encoded, err := json.Marshal(commandResult{
		ExitCode: -1,
		Stdout:   job.CapOutput(stdout),
		Stderr:   job.CapOutput(stderr),
		Canceled: true,
	})
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("%s: encode result: %w", t.name, err)
	}
	return t.result(string(encoded)), nil
}

func (t *Tool) result(content string) dora.ToolResult {
	return dora.ToolResult{Content: content}
}

type input struct {
	Command     string `json:"command"`
	WaitSeconds *int   `json:"wait_seconds,omitempty"`
}

func decodeInput(name string, raw json.RawMessage) (input, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value input
	if err := decoder.Decode(&value); err != nil {
		return input{}, fmt.Errorf("%s: decode input: %w", name, err)
	}
	if value.Command == "" {
		return input{}, fmt.Errorf("%s: command is required", name)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return input{}, fmt.Errorf("%s: input must contain one JSON value", name)
		}
		return input{}, fmt.Errorf("%s: decode input: %w", name, err)
	}
	return value, nil
}

type commandResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// Canceled reports that the run was canceled while the command was still in
	// its foreground wait; the process tree was killed and the output is partial.
	Canceled bool `json:"canceled,omitempty"`
}

var _ dora.Tool = (*Tool)(nil)
