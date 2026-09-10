package cli

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/paths"
	"github.com/lgxz/dora/internal/progress"
	"github.com/lgxz/dora/model/registry"
	"github.com/lgxz/dora/model/router"
	sqlitesession "github.com/lgxz/dora/session/sqlite"
)

func loadRuntimeConfig(opts options, httpClient *http.Client) (config.Config, *router.Router, error) {
	cfg, err := loadConfig(opts)
	if err != nil {
		return config.Config{}, nil, err
	}
	r, err := buildRuntimeRouter(opts, cfg, httpClient)
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, r, nil
}

func loadConfig(opts options) (config.Config, error) {
	configPath := opts.configPath
	configExplicit := configPath != ""
	if configPath == "" {
		var err error
		configPath, err = paths.ConfigFile()
		if err != nil {
			return config.Config{}, err
		}
	}
	cfg, err := config.Load(configPath)
	if !configExplicit && errors.Is(err, os.ErrNotExist) {
		cfg, err = config.Default()
	}
	if err != nil {
		return config.Config{}, err
	}
	if opts.model != "" {
		if _, _, err := parseModelSpec(opts.model); err != nil {
			return config.Config{}, fmt.Errorf("invalid -m %q (expected PROVIDER or PROVIDER/PROFILE): %w", opts.model, err)
		}
	}
	if opts.thinking != "" {
		switch opts.thinking {
		case "off", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return config.Config{}, errors.New(`--thinking must be one of "off", "minimal", "low", "medium", "high", "xhigh", "max"`)
		}
	}
	if opts.maxRoundsSet {
		cfg.Agent.MaxRounds = opts.maxRounds
	}
	return cfg, nil
}

func buildRuntimeRouter(opts options, cfg config.Config, httpClient *http.Client) (*router.Router, error) {
	catalogConfig := registryFromConfig(cfg, httpClient)
	textConstraints := buildTextConstraints(cfg)
	if opts.model != "" {
		provider, profile, err := parseModelSpec(opts.model)
		if err != nil {
			return nil, fmt.Errorf("invalid -m %q (expected PROVIDER or PROVIDER/PROFILE): %w", opts.model, err)
		}
		textConstraints.Provider = provider
		textConstraints.Profile = profile
		addModelIDFallback(&catalogConfig, provider, profile)
	}
	cat, err := registry.NewCatalog(catalogConfig)
	if err != nil {
		return nil, err
	}
	imageConstraints := dora.Constraints{
		Provider: cfg.Policy.Image.Provider,
		Profile:  cfg.Policy.Image.Profile,
		Needs:    []dora.Capability{dora.CapabilityImageInput},
	}
	r, err := router.New(cat, textConstraints, imageConstraints)
	if err != nil {
		return nil, err
	}
	if opts.thinking != "" {
		value := opts.thinking
		if err := r.SetThinking(&value); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// addModelIDFallback adds a transient text profile only for an explicit CLI
// name absent from the selected provider. Existing profiles remain authoritative,
// including their capability restrictions. Policy selection never calls this.
func addModelIDFallback(cfg *registry.Config, provider, name string) {
	if name == "" {
		return
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Name != provider {
			continue
		}
		for _, profile := range p.Profiles {
			if profile.Name == name {
				return
			}
		}
		defaults := config.DefaultProfile(name)
		p.Profiles = append(p.Profiles, registry.Profile{
			Name: name, Model: name,
			MaxTokens: defaults.MaxTokens, ContextWindow: defaults.ContextWindow,
			Capabilities: []dora.Capability{dora.CapabilityText},
		})
		return
	}
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
// profile or model ID components. Supported forms are "provider/profile", "provider/" and
// "provider" (the latter two select the provider's default profile). Everything
// after the first slash is preserved verbatim as the profile or model ID.
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

// systemPrompt appends an environment snapshot to the configured or default
// instructions. The local start date stays fixed for the Agent's lifetime.
func systemPrompt(agent config.Agent, startedAt time.Time) string {
	base := strings.TrimSpace(agent.SystemPrompt)
	if base == "" {
		base = defaultSystemPrompt
	}
	return strings.TrimSpace(base) + fmt.Sprintf("\n\n<runtime_environment>\nOS: %s\nArchitecture: %s\nAgent start date (local): %s\n</runtime_environment>",
		runtime.GOOS, runtime.GOARCH, startedAt.Format(time.DateOnly))
}
