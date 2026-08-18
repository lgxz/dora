// Package registry assembles a dora.Model from a catalog of providers and
// model profiles. It owns the neutral catalog input types and a Construct
// function that turns one resolved provider+profile into a concrete dora.Model
// (translating the neutral "thinking" control into each adapter's wire format).
//
// The registry does not perform selection. Constraint-based selection lives in
// model/router; the registry only preserves catalog order (provider order is
// priority) and constructs adapters.
//
// The registry does not depend on internal/config; callers convert their
// configuration into the registry's neutral input types.
package registry

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/model/openai"
	"github.com/lgxz/dora/model/openairesponses"
)

// ProviderConfig describes one provider endpoint and its models.
type ProviderConfig struct {
	Name                     string
	BaseURL                  string
	APIKey                   string
	API                      string // provider-level default: "chat_completions" | "responses"
	TimeoutSeconds           int
	ConnectTimeoutSeconds    int
	StreamIdleTimeoutSeconds int
	HTTPClient               *http.Client
	Models                   []ModelConfig
}

// ModelConfig describes one named model profile under a provider. Name selects
// the profile; Model is the model identifier sent to the provider.
type ModelConfig struct {
	Name          string
	Model         string
	API           string // overrides provider API when non-empty
	Thinking      *string
	MaxTokens     *int
	ContextWindow *int
	Temperature   *float64
	Capabilities  []dora.Capability
	// Vision is a temporary compatibility field derived from Capabilities. It is
	// removed once internal/cli stops deriving view_image registration from it.
	Vision bool
}

// Config is the registry input.
type Config struct {
	Providers        []ProviderConfig
	SelectedProvider string
	SelectedProfile  string
}

// Selection describes the chosen provider, profile, and effective model. It is
// kept only as a temporary compatibility type for internal/cli; selection moves
// to model/router and this type is removed in the final wiring.
type Selection struct {
	Provider      string
	Profile       string
	API           string
	Model         string
	BaseURL       string
	Vision        bool
	Thinking      *string
	ContextWindow *int
}

// Catalog is an immutable, ordered provider catalog. Provider order and, within
// each provider, model order encode user priority.
type Catalog struct {
	providers []ProviderConfig
}

// NewCatalog validates cfg, preserves order, and returns a Catalog.
func NewCatalog(cfg Config) (*Catalog, error) {
	if len(cfg.Providers) == 0 {
		return nil, errors.New("registry: at least one provider is required")
	}
	for _, p := range cfg.Providers {
		if len(p.Models) == 0 {
			return nil, fmt.Errorf("registry: provider %q has no models configured", p.Name)
		}
		for i := range p.Models {
			if p.Models[i].Model == "" {
				p.Models[i].Model = p.Models[i].Name
			}
		}
	}
	// Copy to keep the catalog immutable against caller mutation.
	providers := make([]ProviderConfig, len(cfg.Providers))
	copy(providers, cfg.Providers)
	return &Catalog{providers: providers}, nil
}

// Providers returns the providers in catalog order.
func (c *Catalog) Providers() []ProviderConfig {
	return c.providers
}

// Construct instantiates a dora.Model for ONE resolved provider+model profile,
// translating the neutral thinking control into the adapter's wire format.
func Construct(p ProviderConfig, m ModelConfig) (dora.Model, error) {
	api := m.API
	if api == "" {
		api = p.API
	}
	dur := func(seconds int) time.Duration { return time.Duration(seconds) * time.Second }
	switch api {
	case "chat_completions":
		reasoningEffort, thinking := mapChatThinking(p.Name, m.Thinking)
		return openai.New(openai.Config{
			BaseURL:           p.BaseURL,
			APIKey:            p.APIKey,
			Model:             m.Model,
			HTTPClient:        p.HTTPClient,
			ConnectTimeout:    dur(p.ConnectTimeoutSeconds),
			StreamIdleTimeout: dur(p.StreamIdleTimeoutSeconds),
			Timeout:           dur(p.TimeoutSeconds),
			MaxTokens:         m.MaxTokens,
			Temperature:       m.Temperature,
			ReasoningEffort:   reasoningEffort,
			Thinking:          thinking,
		})
	case "responses":
		reasoning := mapResponsesThinking(p.Name, m.Thinking)
		return openairesponses.New(openairesponses.Config{
			BaseURL:           p.BaseURL,
			APIKey:            p.APIKey,
			Model:             m.Model,
			HTTPClient:        p.HTTPClient,
			ConnectTimeout:    dur(p.ConnectTimeoutSeconds),
			StreamIdleTimeout: dur(p.StreamIdleTimeoutSeconds),
			Timeout:           dur(p.TimeoutSeconds),
			MaxTokens:         m.MaxTokens,
			Temperature:       m.Temperature,
			Reasoning:         reasoning,
		})
	default:
		return nil, fmt.Errorf("registry: unknown API %q for provider %q", api, p.Name)
	}
}

// Registry holds a resolved provider+profile selection. It is a temporary
// compatibility shim that delegates construction to Construct; it is removed
// once internal/cli is rewired to model/router.
type Registry struct {
	provider ProviderConfig
	model    ModelConfig
}

// New validates cfg, selects a provider and profile, and returns a Registry.
func New(cfg Config) (*Registry, error) {
	if len(cfg.Providers) == 0 {
		return nil, errors.New("registry: at least one provider is required")
	}
	provider, err := selectProvider(cfg)
	if err != nil {
		return nil, err
	}
	if len(provider.Models) == 0 {
		return nil, fmt.Errorf("registry: provider %q has no models configured", provider.Name)
	}
	model, err := selectProfile(provider, cfg.SelectedProfile)
	if err != nil {
		return nil, err
	}
	if model.Model == "" {
		model.Model = model.Name
	}
	return &Registry{provider: provider, model: model}, nil
}

func selectProvider(cfg Config) (ProviderConfig, error) {
	if cfg.SelectedProvider != "" {
		for _, p := range cfg.Providers {
			if p.Name == cfg.SelectedProvider {
				return p, nil
			}
		}
		return ProviderConfig{}, fmt.Errorf("registry: selected provider %q not found", cfg.SelectedProvider)
	}
	if len(cfg.Providers) == 1 {
		return cfg.Providers[0], nil
	}
	var keyed []ProviderConfig
	for _, provider := range cfg.Providers {
		if provider.APIKey != "" {
			keyed = append(keyed, provider)
		}
	}
	if len(keyed) == 1 {
		return keyed[0], nil
	}
	if len(keyed) > 1 {
		names := make([]string, len(keyed))
		for i, provider := range keyed {
			names[i] = provider.Name
		}
		return ProviderConfig{}, fmt.Errorf("registry: API keys are configured for multiple providers (%s); set DORA_MODEL or client.provider", strings.Join(names, ", "))
	}
	names := make([]string, len(cfg.Providers))
	for i, p := range cfg.Providers {
		names[i] = p.Name
	}
	return ProviderConfig{}, fmt.Errorf("registry: multiple providers available (%s); set client.provider to select one", strings.Join(names, ", "))
}

func selectProfile(provider ProviderConfig, selected string) (ModelConfig, error) {
	if selected != "" {
		for _, m := range provider.Models {
			if m.Name == selected {
				return m, nil
			}
		}
		return ModelConfig{}, fmt.Errorf("registry: profile %q not found under provider %q", selected, provider.Name)
	}
	return provider.Models[0], nil
}

// Selection returns the resolved provider, profile, and model metadata. The
// Vision field is derived from Capabilities for compatibility until the CLI is
// rewired.
func (r *Registry) Selection() Selection {
	api := r.model.API
	if api == "" {
		api = r.provider.API
	}
	return Selection{
		Provider:      r.provider.Name,
		Profile:       r.model.Name,
		API:           api,
		Model:         r.model.Model,
		BaseURL:       strings.TrimRight(r.provider.BaseURL, "/"),
		Vision:        hasCapability(r.model.Capabilities, dora.CapabilityImageInput),
		Thinking:      r.model.Thinking,
		ContextWindow: r.model.ContextWindow,
	}
}

// SetThinking overrides the selected profile's thinking mode (for --thinking).
func (r *Registry) SetThinking(thinking *string) { r.model.Thinking = thinking }

// Model instantiates the concrete dora.Model via Construct.
func (r *Registry) Model() (dora.Model, error) {
	return Construct(r.provider, r.model)
}

// hasCapability reports whether capabilities contains the requested capability.
func hasCapability(capabilities []dora.Capability, want dora.Capability) bool {
	for _, c := range capabilities {
		if c == want {
			return true
		}
	}
	return false
}
