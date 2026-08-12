// Package powershell implements a dora.Tool that executes PowerShell commands.
package powershell

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/tool/internal/commandexec"
)

// ErrUnavailable indicates that PowerShell is not installed or cannot be
// found on the executable search path.
var ErrUnavailable = errors.New("powershell: executable unavailable")

// Config controls PowerShell command execution.
type Config struct {
	Timeout        time.Duration
	MaxOutputBytes int
	// Vision advertises the @@path@@ image tag in the tool description.
	Vision bool
}

// Tool executes commands using PowerShell.
type Tool struct {
	core *commandexec.Tool
}

// New creates a PowerShell tool. It prefers PowerShell Core (pwsh) and falls
// back to Windows PowerShell. Commands inherit the process's current directory.
func New(cfg Config) (*Tool, error) {
	binary, err := findExecutable()
	if err != nil {
		return nil, err
	}

	core, err := commandexec.New(commandexec.Config{
		Name:           "powershell",
		Description:    "Execute PowerShell command.",
		Binary:         binary,
		CommandArgs:    commandArgs,
		Timeout:        cfg.Timeout,
		MaxOutputBytes: cfg.MaxOutputBytes,
		Vision:         cfg.Vision,
	})
	if err != nil {
		return nil, err
	}
	return &Tool{core: core}, nil
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
	return t.core.Spec()
}

// Execute implements dora.Tool. Command failures are returned as structured
// output so the model can inspect stderr and decide how to proceed.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if t == nil {
		return "", errors.New("powershell: tool is not initialized")
	}
	return t.core.Execute(ctx, raw)
}

func commandArgs(command string) []string {
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command}
}

var _ dora.Tool = (*Tool)(nil)
