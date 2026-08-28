package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/paths"
	"github.com/lgxz/dora/internal/progress"
	"github.com/lgxz/dora/model/registry"
	"github.com/lgxz/dora/model/router"
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
	textConstraints := buildTextConstraints(cfg)
	if opts.model != "" {
		provider, profile, err := parseModelSpec(opts.model)
		if err != nil {
			return config.Config{}, nil, fmt.Errorf("invalid -m %q (expected PROVIDER or PROVIDER/PROFILE): %w", opts.model, err)
		}
		textConstraints.Provider = provider
		textConstraints.Profile = profile
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
		case "off", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return config.Config{}, nil, errors.New(`--thinking must be one of "off", "minimal", "low", "medium", "high", "xhigh", "max"`)
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

// buildTextConstraints constructs the text model constraints from the
// configured policy. The -m flag may later override the provider and profile
// on the returned value.
func buildTextConstraints(cfg config.Config) dora.Constraints {
	return dora.Constraints{
		Provider: cfg.Policy.Text.Provider,
		Profile:  cfg.Policy.Text.Profile,
		Needs:    []dora.Capability{dora.CapabilityText},
	}
}

// parseModelSpec splits a PROVIDER/PROFILE model override into its provider and
// profile components. Supported forms are "provider/profile", "provider/" and
// "provider" (the latter two select the provider's default profile). Everything
// else returns an error.
func parseModelSpec(s string) (provider, profile string, err error) {
	if s == "" {
		return "", "", errors.New("empty model spec")
	}
	slash := strings.IndexByte(s, '/')
	if slash < 0 {
		return s, "", nil
	}
	provider = s[:slash]
	profile = s[slash+1:]
	if provider == "" {
		return "", "", errors.New("missing provider")
	}
	if strings.Contains(profile, "/") {
		return "", "", errors.New("model spec must contain at most one '/'")
	}
	return provider, profile, nil
}

func openSession(ctx context.Context, path string) (*sqlitesession.Store, error) {
	if path == "" {
		return sqlitesession.OpenMemory(ctx)
	}
	store, err := sqlitesession.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func buildObserver(streams IO, quiet, reasoning bool, colorMode, sessionPath string) *progress.Renderer {
	if quiet {
		return nil
	}
	color := streams.ColorProgress
	switch colorMode {
	case "always":
		color = true
	case "never":
		color = false
	}
	renderer := progress.New(streams.Stderr, streams.TerminalProgress, color, reasoning)
	if sessionPath != "" {
		renderer.Session(sessionPath)
	}
	return renderer
}

// info emits an informational line through the observer, so it is silenced by
// --quiet (a nil observer). Informational output must go through this channel
// instead of printing directly to stderr.
func info(observer dora.Observer, format string, args ...any) {
	if observer == nil {
		return
	}
	observer.Observe(dora.Update{Kind: dora.UpdateInfo, Info: fmt.Sprintf(format, args...)})
}

//go:embed prompts/default_system.md
var defaultSystemPrompt string

// systemPrompt returns the immutable Agent system prompt: the configured
// agent.system_prompt when set (fully replacing the built-in default) or the
// built-in default otherwise.
func systemPrompt(agent config.Agent) string {
	base := strings.TrimSpace(agent.SystemPrompt)
	if base == "" {
		base = defaultSystemPrompt
	}
	return base
}
