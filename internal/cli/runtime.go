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

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/paths"
	"github.com/lgxz/dora/internal/progress"
	"github.com/lgxz/dora/model/registry"
	"github.com/lgxz/dora/model/router"
	jsonlsession "github.com/lgxz/dora/session/jsonl"
	"github.com/lgxz/dora/skill"
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

// openSession opens the selected session. An empty path is an in-memory
// session. workdir is recorded in the header of a file that does not exist yet.
func openSession(ctx context.Context, path, workdir string) (*jsonlsession.Store, error) {
	if path == "" {
		return jsonlsession.OpenMemory(ctx)
	}
	return jsonlsession.Open(ctx, path, jsonlsession.WithWorkdir(workdir))
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

// systemPrompt returns the configured or default instructions. It holds only
// what depends on the Dora version and the configuration, so that it and the
// tool definitions stay an unchanging request prefix. Host facts and the
// available skills travel in agentContext, and the date and working directory
// in app.RunContext, both in front of the user input.
func systemPrompt(agent config.Agent) string {
	base := strings.TrimSpace(agent.SystemPrompt)
	if base == "" {
		base = defaultSystemPrompt
	}
	return strings.TrimSpace(base)
}

// agentContext is the per-installation context placed in front of every
// Turn's user input: the host first, which seldom changes, then the skills,
// which change only when the user adds or removes one. Volatile facts such as
// the date belong to app.RunContext instead.
func agentContext(tools []dora.Tool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<environment>\nOS: %s\nArchitecture: %s", runtime.GOOS, runtime.GOARCH)
	for _, tool := range tools {
		if catalog := skill.Catalog(tool); catalog != "" {
			b.WriteString("\n\n" + catalog)
		}
	}
	b.WriteString("\n</environment>")
	return b.String()
}
