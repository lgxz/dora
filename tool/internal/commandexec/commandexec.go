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
)

const (
	defaultTimeout        = 120 * time.Second
	maxTimeout            = time.Hour
	maxTimeoutSeconds     = int(maxTimeout / time.Second)
	defaultMaxOutputBytes = 1 << 20
)

// Config describes a command tool and controls its execution.
type Config struct {
	Name           string
	Description    string
	Binary         string
	CommandArgs    func(string) []string
	Timeout        time.Duration
	MaxOutputBytes int
}

// Tool executes commands using a configured executable.
type Tool struct {
	name           string
	description    string
	binary         string
	commandArgs    func(string) []string
	timeout        time.Duration
	maxOutputBytes int
}

// New creates a command execution tool.
func New(cfg Config) (*Tool, error) {
	timeout := cfg.Timeout
	if timeout < 0 {
		return nil, fmt.Errorf("%s: timeout cannot be negative", cfg.Name)
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout {
		return nil, fmt.Errorf("%s: timeout cannot exceed %s", cfg.Name, maxTimeout)
	}
	maxOutputBytes := cfg.MaxOutputBytes
	if maxOutputBytes < 0 {
		return nil, fmt.Errorf("%s: maximum output bytes cannot be negative", cfg.Name)
	}
	if maxOutputBytes == 0 {
		maxOutputBytes = defaultMaxOutputBytes
	}

	return &Tool{
		name:           cfg.Name,
		description:    cfg.Description,
		binary:         cfg.Binary,
		commandArgs:    cfg.CommandArgs,
		timeout:        timeout,
		maxOutputBytes: maxOutputBytes,
	}, nil
}

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	timeoutDescription := fmt.Sprintf(
		"Maximum execution time for this command. Omit to use the default of %s.",
		t.timeout,
	)
	return dora.ToolSpec{
		Name:        t.name,
		Description: t.description,
		InputSchema: json.RawMessage(fmt.Sprintf(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "command"
    },
    "timeout_seconds": {
      "type": "integer",
      "minimum": 1,
      "maximum": 3600,
      "description": %q
    }
  },
  "required": ["command"],
  "additionalProperties": false
}`, timeoutDescription)),
	}
}

// Execute implements dora.Tool. Command failures are returned as structured
// output so the model can inspect stderr and decide how to proceed.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	input, err := decodeInput(t.name, raw)
	if err != nil {
		return "", err
	}

	timeout := t.timeout
	if input.TimeoutSeconds != nil {
		timeout = time.Duration(*input.TimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stdout := newLimitedBuffer(t.maxOutputBytes)
	stderr := newLimitedBuffer(t.maxOutputBytes)
	command := exec.CommandContext(runCtx, t.binary, t.commandArgs(input.Command)...)
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
			return "", fmt.Errorf("%s: execute command: %w", t.name, runErr)
		}
		result.ExitCode = exitError.ExitCode()
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("%s: encode result: %w", t.name, err)
	}
	return string(encoded), nil
}

type input struct {
	Command        string `json:"command"`
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty"`
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
	if value.TimeoutSeconds != nil && *value.TimeoutSeconds < 1 {
		return input{}, fmt.Errorf("%s: timeout_seconds must be positive", name)
	}
	if value.TimeoutSeconds != nil && *value.TimeoutSeconds > maxTimeoutSeconds {
		return input{}, fmt.Errorf("%s: timeout_seconds cannot exceed %d", name, maxTimeoutSeconds)
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

var _ dora.Tool = (*Tool)(nil)
