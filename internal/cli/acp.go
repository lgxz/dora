package cli

import (
	"context"
	"errors"

	acpserver "github.com/lgxz/dora/internal/acp"
	"github.com/lgxz/dora/internal/app"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/model/router"
	sqlitesession "github.com/lgxz/dora/session/sqlite"
)

func runACP(ctx context.Context, opts options, cfg config.Config, streams IO) error {
	if err := validateACPOptions(opts); err != nil {
		return err
	}
	model, err := buildRuntimeRouter(opts, cfg, streams.HTTPClient)
	if err != nil && !errors.Is(err, router.ErrNotFound) {
		return err
	}

	return acpserver.Serve(ctx, streams.Stdin, streams.Stdout, acpserver.Config{
		Version: streams.BuildVersion,
		NewSession: func(sessionCtx context.Context, cwd string) (*app.Session, error) {
			if model == nil {
				return nil, acpserver.AuthenticationRequired()
			}
			workdir, err := resolveWorkingDirectory(cwd)
			if err != nil {
				return nil, err
			}
			store, err := sqlitesession.OpenMemory(sessionCtx)
			if err != nil {
				return nil, err
			}
			jobs := job.New()
			agent, err := buildAgent(cfg, agentDependencies{
				model:        model,
				jobs:         jobs,
				history:      store,
				noSkills:     opts.noSkills,
				systemPrompt: systemPrompt(cfg.Agent),
			})
			if err != nil {
				_ = store.Close()
				return nil, err
			}
			application, err := app.NewSession(agent, store, jobs, workdir)
			if err != nil {
				_ = store.Close()
				return nil, err
			}
			return application, nil
		},
	})
}

func validateACPOptions(opts options) error {
	switch {
	case len(opts.promptArgs) != 0:
		return errors.New("--acp does not accept a prompt")
	case opts.sessionPath != "":
		return errors.New("--acp does not accept --session; ACP sessions manage their own history")
	case opts.workdir != "":
		return errors.New("--acp does not accept --workdir; the ACP client supplies cwd per session")
	case opts.events:
		return errors.New("--acp cannot be combined with --events")
	}
	return nil
}
