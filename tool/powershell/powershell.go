// Package powershell implements a dora.Tool that executes PowerShell commands.
package powershell

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
	defaultTimeout        = 30 * time.Second
	maxTimeout            = time.Hour
	maxTimeoutSeconds     = int(maxTimeout / time.Second)
	defaultMaxOutputBytes = 1 << 20
)

// ErrUnavailable indicates that PowerShell is not installed or cannot be
// found on the executable search path.
var ErrUnavailable = errors.New("powershell: executable unavailable")

// Config controls PowerShell command execution.
type Config struct {
	Timeout        time.Duration
	MaxOutputBytes int
}

// Tool executes commands using PowerShell.
type Tool struct {
	binary         string
	timeout        time.Duration
	maxOutputBytes int
}

// New creates a PowerShell tool. It prefers PowerShell Core (pwsh) and falls
// back to Windows PowerShell. Commands inherit the process's current directory.
func New(cfg Config) (*Tool, error) {
	binary, err := findExecutable()
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout < 0 {
		return nil, errors.New("powershell: timeout cannot be negative")
	}
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout > maxTimeout {
		return nil, fmt.Errorf("powershell: timeout cannot exceed %s", maxTimeout)
	}
	maxOutputBytes := cfg.MaxOutputBytes
	if maxOutputBytes < 0 {
		return nil, errors.New("powershell: maximum output bytes cannot be negative")
	}
	if maxOutputBytes == 0 {
		maxOutputBytes = defaultMaxOutputBytes
	}

	return &Tool{
		binary:         binary,
		timeout:        timeout,
		maxOutputBytes: maxOutputBytes,
	}, nil
}

func findExecutable() (string, error) {
	for _, name := range []string{"pwsh", "powershell.exe"} {
		binary, err := exec.LookPath(name)
		if err == nil {
			return binary, nil
		}
	}
	return "", ErrUnavailable
}

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	timeoutDescription := fmt.Sprintf(
		"Maximum execution time for this command. Omit to use the default of %s.",
		t.timeout,
	)
	return dora.ToolSpec{
		Name:        "powershell",
		Description: "Execute a PowerShell command.",
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
	if t == nil {
		return "", errors.New("powershell: tool is not initialized")
	}
	input, err := decodeInput(raw)
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
	args := commandArgs(input.Command)
	command := exec.CommandContext(runCtx, t.binary, args...)
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
			return "", fmt.Errorf("powershell: execute command: %w", runErr)
		}
		result.ExitCode = exitError.ExitCode()
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("powershell: encode result: %w", err)
	}
	return string(encoded), nil
}

func commandArgs(command string) []string {
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command}
}

type input struct {
	Command        string `json:"command"`
	TimeoutSeconds *int   `json:"timeout_seconds,omitempty"`
}

func decodeInput(raw json.RawMessage) (input, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value input
	if err := decoder.Decode(&value); err != nil {
		return input{}, fmt.Errorf("powershell: decode input: %w", err)
	}
	if value.Command == "" {
		return input{}, errors.New("powershell: command is required")
	}
	if value.TimeoutSeconds != nil && *value.TimeoutSeconds < 1 {
		return input{}, errors.New("powershell: timeout_seconds must be positive")
	}
	if value.TimeoutSeconds != nil && *value.TimeoutSeconds > maxTimeoutSeconds {
		return input{}, fmt.Errorf("powershell: timeout_seconds cannot exceed %d", maxTimeoutSeconds)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return input{}, errors.New("powershell: input must contain one JSON value")
		}
		return input{}, fmt.Errorf("powershell: decode input: %w", err)
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
