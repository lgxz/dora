package cli

import (
	"errors"
	"runtime"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/model/registry"
	"github.com/lgxz/dora/session"
	"github.com/lgxz/dora/skill"
	bashtool "github.com/lgxz/dora/tool/bash"
	filetool "github.com/lgxz/dora/tool/file"
	historytool "github.com/lgxz/dora/tool/history"
	jobtool "github.com/lgxz/dora/tool/job"
	powershelltool "github.com/lgxz/dora/tool/powershell"
)

func buildTools(cfg config.Config, selection registry.Selection, manager *job.Manager, history session.Reader, historyAvailable, noSkills bool) ([]dora.Tool, error) {
	var tools []dora.Tool
	if historyAvailable {
		historyTool, err := historytool.New(history)
		if err != nil {
			return nil, err
		}
		tools = append(tools, historyTool)
	}
	if !noSkills {
		skillDirectories, err := configuredSkillDirectories(cfg.Skills.Directories)
		if err != nil {
			return nil, err
		}
		if len(skillDirectories) > 0 {
			skills, err := skill.New(skill.Config{Directories: skillDirectories})
			if errors.Is(err, skill.ErrNoSkills) {
				skills = nil
				err = nil
			}
			if err != nil {
				return nil, err
			}
			if skills != nil {
				tools = append(tools, skills)
			}
		}
	}
	commandTools, err := buildCommandTools(cfg.Tools, selection.Vision, manager)
	if err != nil {
		return nil, err
	}
	tools = append(tools, commandTools...)
	tools = append(tools, jobtool.New(manager, selection.Vision))
	tools = append(tools, buildFileTools()...)
	return tools, nil
}

// buildFileTools returns the read/write/edit/grep/glob file tools.
func buildFileTools() []dora.Tool {
	return []dora.Tool{
		filetool.NewReadTool(),
		filetool.NewWriteTool(),
		filetool.NewEditTool(),
		filetool.NewGrepTool(),
		filetool.NewGlobTool(),
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
