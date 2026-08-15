package cli

import (
	"errors"
	"runtime"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/job"
	bashtool "github.com/lgxz/dora/tool/bash"
	filetool "github.com/lgxz/dora/tool/file"
	powershelltool "github.com/lgxz/dora/tool/powershell"
)

// buildFileTools returns the read/write/edit/grep file tools.
func buildFileTools() []dora.Tool {
	return []dora.Tool{
		filetool.NewReadTool(),
		filetool.NewWriteTool(),
		filetool.NewEditTool(),
		filetool.NewGrepTool(),
	}
}

type toolCandidate struct {
	enabled          *bool
	defaultPlatforms []string
	create           func() (dora.Tool, error)
	unavailable      error
}

func buildCommandTools(cfg config.Tools, vision bool, jobManager *job.Manager) ([]dora.Tool, error) {
	return assembleCommandTools(runtime.GOOS, []toolCandidate{
		{
			enabled:          cfg.Bash.Enabled,
			defaultPlatforms: []string{"darwin", "linux"},
			create: func() (dora.Tool, error) {
				return bashtool.New(bashtool.Config{
					Timeout:    time.Duration(cfg.Bash.TimeoutSeconds) * time.Second,
					Vision:     vision,
					JobManager: jobManager,
				})
			},
			unavailable: bashtool.ErrUnavailable,
		},
		{
			enabled:          cfg.PowerShell.Enabled,
			defaultPlatforms: []string{"windows"},
			create: func() (dora.Tool, error) {
				return powershelltool.New(powershelltool.Config{
					Timeout:    time.Duration(cfg.PowerShell.TimeoutSeconds) * time.Second,
					Vision:     vision,
					JobManager: jobManager,
				})
			},
			unavailable: powershelltool.ErrUnavailable,
		},
	})
}

func assembleCommandTools(goos string, candidates []toolCandidate) ([]dora.Tool, error) {
	var tools []dora.Tool
	for _, candidate := range candidates {
		enabled, explicit := candidate.enabledOn(goos)
		if !enabled {
			continue
		}
		tool, err := candidate.create()
		if err != nil {
			if errors.Is(err, candidate.unavailable) && !explicit {
				continue
			}
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func (candidate toolCandidate) enabledOn(goos string) (enabled bool, explicit bool) {
	if candidate.enabled != nil {
		return *candidate.enabled, true
	}
	for _, platform := range candidate.defaultPlatforms {
		if platform == goos {
			return true, false
		}
	}
	return false, false
}
