package cli

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/paths"
	"github.com/lgxz/dora/internal/progress"
	"github.com/lgxz/dora/model/registry"
	"github.com/lgxz/dora/model/router"
	"github.com/lgxz/dora/session"
	sqlitesession "github.com/lgxz/dora/session/sqlite"
)

func loadRuntimeConfig(opts options, httpClient *http.Client) (config.Config, *router.Router, error) {
	configPath := opts.configPath
	configExplicit := configPath != ""
	if configPath == "" {
		var err error
		configPath, err = paths.ConfigFile()
		if err != nil {
			return config.Config{}, nil, err
		}
	}
	cfg, err := config.Load(configPath)
	if !configExplicit && errors.Is(err, os.ErrNotExist) {
		cfg, err = config.Default()
	}
	if err != nil {
		return config.Config{}, nil, err
	}
	cat, err := registry.NewCatalog(registryFromConfig(cfg, httpClient))
	if err != nil {
		return config.Config{}, nil, err
	}
	textConstraints := dora.Constraints{
		Provider: cfg.Policy.Text.Provider,
		Profile:  cfg.Policy.Text.Profile,
		Needs:    []dora.Capability{dora.CapabilityText},
	}
	imageConstraints := dora.Constraints{
		Provider: cfg.Policy.Image.Provider,
		Profile:  cfg.Policy.Image.Profile,
		Needs:    []dora.Capability{dora.CapabilityImageInput},
	}
	r, err := router.New(cat, textConstraints, imageConstraints)
	if err != nil {
		return config.Config{}, nil, err
	}
	if opts.thinking != "" {
		switch opts.thinking {
		case "off", "minimal", "low", "medium", "high":
		default:
			return config.Config{}, nil, errors.New(`--thinking must be one of "off", "minimal", "low", "medium", "high"`)
		}
		value := opts.thinking
		if err := r.SetThinking(&value); err != nil {
			return config.Config{}, nil, err
		}
	}
	if opts.maxRoundsSet {
		cfg.Agent.MaxRounds = opts.maxRounds
	}
	return cfg, r, nil
}

func openSession(ctx context.Context, path string) (*sqlitesession.Store, bool, error) {
	if path == "" {
		return nil, false, nil
	}
	store, err := sqlitesession.Open(ctx, path)
	if err != nil {
		return nil, false, err
	}
	page, err := store.ListTurns(ctx, session.ListOptions{Limit: 1})
	if err != nil {
		store.Close()
		return nil, false, err
	}
	return store, page.Total > 0, nil
}

func buildObserver(streams IO, quiet bool, sessionPath string) dora.Observer {
	if quiet {
		return nil
	}
	renderer := progress.New(streams.Stderr, streams.TerminalProgress, streams.ColorProgress)
	if sessionPath != "" {
		renderer.Session(sessionPath)
	}
	return renderer
}

func buildTurn(prompt string) *dora.Turn {
	return dora.NewTurn(systemPrompt(), prompt)
}

// systemPrompt returns the content of <doraHome>/AGENTS.md when it exists, or
// an empty string otherwise. An empty result means no system message is sent.
func systemPrompt() string {
	path, err := paths.AgentsFile()
	if err != nil {
		return ""
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}
