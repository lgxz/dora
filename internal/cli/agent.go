package cli

import (
	"context"
	"errors"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/model/router"
	"github.com/lgxz/dora/session"
	tasktool "github.com/lgxz/dora/tool/task"
)

type agentDependencies struct {
	model        *router.Router
	jobs         *job.Manager
	history      session.Reader
	noSkills     bool
	extraTools   []dora.Tool
	systemPrompt string
}

func buildAgent(cfg config.Config, deps agentDependencies) (*dora.Agent, error) {
	var agent *dora.Agent
	tools, err := buildTools(cfg, toolDependencies{
		model:      deps.model,
		jobs:       deps.jobs,
		history:    deps.history,
		noSkills:   deps.noSkills,
		extraTools: deps.extraTools,
		taskRunner: func(ctx context.Context, instruction string) (string, error) {
			return runTask(ctx, agent, instruction)
		},
	})
	if err != nil {
		return nil, err
	}
	agent, err = dora.NewWithConfig(deps.model, dora.AgentConfig{
		MaxRounds:    cfg.Agent.MaxRounds,
		SystemPrompt: deps.systemPrompt,
	}, tools...)
	return agent, err
}

func runTask(ctx context.Context, agent *dora.Agent, instruction string) (string, error) {
	if agent == nil {
		return "", errors.New("task agent is not initialized")
	}
	turn := dora.NewTurn(instruction)
	if err := agent.RunObservedWithOptions(ctx, turn, nil, dora.RunOptions{
		ExcludeTools: []string{tasktool.Name},
	}); err != nil {
		return "", err
	}
	result, complete := turn.Result()
	if !complete {
		return "", errors.New("task turn completed without a result")
	}
	return result, nil
}
