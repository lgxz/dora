// Package bash implements a dora.Tool that executes Bash commands.
package bash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/tool/internal/commandexec"
)

// ErrUnavailable indicates that Bash is not installed or cannot be found on
// the executable search path.
var ErrUnavailable = errors.New("bash: executable unavailable")

// Config controls Bash command execution.
type Config struct {
	Timeout        time.Duration
	MaxOutputBytes int
	// JobManager, when set, enables background execution via wait_seconds.
	JobManager *job.Manager
	// Vision advertises the @@path@@ image tag in the tool description.
	Vision bool
}

// Tool executes commands using Bash.
type Tool struct {
	core *commandexec.Tool
}

// New creates a Bash tool. Commands inherit the process's current directory.
// Zero timeout and output limit values use safe defaults.
func New(cfg Config) (*Tool, error) {
	binary, err := exec.LookPath("bash")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	core, err := commandexec.New(commandexec.Config{
		Name:           "bash",
		Description:    fmt.Sprintf("Execute Bash command on %s/%s", runtime.GOOS, runtime.GOARCH),
		Binary:         binary,
		CommandArgs:    commandArgs,
		Timeout:        cfg.Timeout,
		MaxOutputBytes: cfg.MaxOutputBytes,
		JobManager:     cfg.JobManager,
		Vision:         cfg.Vision,
	})
	if err != nil {
		return nil, err
	}
	return &Tool{core: core}, nil
}

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	return t.core.Spec()
}

// Execute implements dora.Tool. Command failures are returned as structured
// output so the model can inspect stderr and decide how to proceed.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	if t == nil {
		return dora.ToolResult{}, errors.New("bash: tool is not initialized")
	}
	return t.core.Execute(ctx, raw)
}

func commandArgs(command string) []string {
	return []string{"-lc", command}
}

var _ dora.Tool = (*Tool)(nil)
