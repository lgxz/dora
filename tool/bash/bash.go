// Package bash implements a dora.Tool that executes Bash commands.
package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"dora"
)

const (
	defaultTimeout        = 30 * time.Second
	defaultMaxOutputBytes = 1 << 20
)

// Config controls Bash command execution.
type Config struct {
	WorkingDir     string
	Timeout        time.Duration
	MaxOutputBytes int
}

// Tool executes commands using Bash.
type Tool struct {
	binary         string
	workingDir     string
	timeout        time.Duration
	maxOutputBytes int
}

// New creates a Bash tool. An empty working directory uses the process's
// current directory. Zero timeout and output limit values use safe defaults.
func New(cfg Config) (*Tool, error) {
	binary, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("bash: find executable: %w", err)
	}

	workingDir := cfg.WorkingDir
	if workingDir == "" {
		workingDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("bash: get working directory: %w", err)
		}
	}
	workingDir, err = filepath.Abs(workingDir)
	if err != nil {
		return nil, fmt.Errorf("bash: resolve working directory: %w", err)
	}
	info, err := os.Stat(workingDir)
	if err != nil {
		return nil, fmt.Errorf("bash: inspect working directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("bash: working directory %q is not a directory", workingDir)
	}

	timeout := cfg.Timeout
	if timeout < 0 {
		return nil, errors.New("bash: timeout cannot be negative")
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	maxOutputBytes := cfg.MaxOutputBytes
	if maxOutputBytes < 0 {
		return nil, errors.New("bash: maximum output bytes cannot be negative")
	}
	if maxOutputBytes == 0 {
		maxOutputBytes = defaultMaxOutputBytes
	}

	return &Tool{
		binary:         binary,
		workingDir:     workingDir,
		timeout:        timeout,
		maxOutputBytes: maxOutputBytes,
	}, nil
}

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "bash",
		Description: "Execute a Bash command in the configured working directory.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The Bash command to execute."
    }
  },
  "required": ["command"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool. Command failures are returned as structured
// output so the model can inspect stderr and decide how to proceed.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if t == nil {
		return "", errors.New("bash: tool is not initialized")
	}

	input, err := decodeInput(raw)
	if err != nil {
		return "", err
	}

	runCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	stdout := newLimitedBuffer(t.maxOutputBytes)
	stderr := newLimitedBuffer(t.maxOutputBytes)
	command := exec.CommandContext(runCtx, t.binary, "-lc", input.Command)
	command.Dir = t.workingDir
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = time.Second

	runErr := command.Run()
	if err := ctx.Err(); err != nil {
		return "", err
	}

	result := commandResult{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Truncated: stdout.Truncated() || stderr.Truncated(),
	}

	switch {
	case runErr == nil:
		result.ExitCode = 0
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		result.ExitCode = -1
		result.TimedOut = true
	default:
		var exitError *exec.ExitError
		if !errors.As(runErr, &exitError) {
			return "", fmt.Errorf("bash: execute command: %w", runErr)
		}
		result.ExitCode = exitError.ExitCode()
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("bash: encode result: %w", err)
	}
	return string(encoded), nil
}

type input struct {
	Command string `json:"command"`
}

func decodeInput(raw json.RawMessage) (input, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value input
	if err := decoder.Decode(&value); err != nil {
		return input{}, fmt.Errorf("bash: decode input: %w", err)
	}
	if value.Command == "" {
		return input{}, errors.New("bash: command is required")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return input{}, errors.New("bash: input must contain one JSON value")
		}
		return input{}, fmt.Errorf("bash: decode input: %w", err)
	}
	return value, nil
}

type commandResult struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{remaining: limit}
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	if len(value) > b.remaining {
		value = value[:b.remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(value)
	b.remaining -= len(value)
	return written, nil
}

func (b *limitedBuffer) String() string { return b.buffer.String() }

func (b *limitedBuffer) Truncated() bool { return b.truncated }
