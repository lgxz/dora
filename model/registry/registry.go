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
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/model/openai"
	"github.com/lgxz/dora/model/openairesponses"
)

// ProviderConfig describes one provider endpoint and its profiles.
type ProviderConfig struct {
	Name                     string
	BaseURL                  string
	APIKey                   string
	API                      string // provider-level default: "chat_completions" | "responses"
	TimeoutSeconds           int
	ConnectTimeoutSeconds    int
	StreamIdleTimeoutSeconds int
	HTTPClient               *http.Client
	Profiles                 []Profile
}

// Profile describes one named model profile under a provider. Name selects
// the profile; Model is the model identifier sent to the provider.
type Profile struct {
	Name             string
	Model            string
	API              string // overrides provider API when non-empty
	Thinking         *string
	PreserveThinking *bool
	MaxTokens        *int
	MaxOutputTokens  *int
	ContextWindow    *int
	Temperature      *float64
	Capabilities     []dora.Capability
}

// Config is the registry input.
type Config struct {
	Providers []ProviderConfig
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
		if len(p.Profiles) == 0 {
			return nil, fmt.Errorf("registry: provider %q has no profiles configured", p.Name)
		}
		for i := range p.Profiles {
			if p.Profiles[i].Model == "" {
				p.Profiles[i].Model = p.Profiles[i].Name
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

// Construct instantiates a dora.Model for ONE resolved provider+profile,
// translating the neutral thinking control into the adapter's wire format.
func Construct(p ProviderConfig, profile Profile) (dora.Model, error) {
	api := profile.API
	if api == "" {
		api = p.API
	}
	dur := func(seconds int) time.Duration { return time.Duration(seconds) * time.Second }
	switch api {
	case "chat_completions":
		reasoningEffort, thinking := mapChatThinking(p.Name, profile.Thinking)
		return openai.New(openai.Config{
			BaseURL:           p.BaseURL,
			APIKey:            p.APIKey,
			Model:             profile.Model,
			HTTPClient:        p.HTTPClient,
			ConnectTimeout:    dur(p.ConnectTimeoutSeconds),
			StreamIdleTimeout: dur(p.StreamIdleTimeoutSeconds),
			Timeout:           dur(p.TimeoutSeconds),
			MaxTokens:         profile.MaxTokens,
			MaxOutputTokens:   profile.MaxOutputTokens,
			Temperature:       profile.Temperature,
			ReasoningEffort:   reasoningEffort,
			Thinking:          thinking,
			PreserveThinking:  profile.PreserveThinking,
		})
	case "responses":
		reasoning := mapResponsesThinking(p.Name, profile.Thinking)
		return openairesponses.New(openairesponses.Config{
			BaseURL:           p.BaseURL,
			APIKey:            p.APIKey,
			Model:             profile.Model,
			HTTPClient:        p.HTTPClient,
			ConnectTimeout:    dur(p.ConnectTimeoutSeconds),
			StreamIdleTimeout: dur(p.StreamIdleTimeoutSeconds),
			Timeout:           dur(p.TimeoutSeconds),
			MaxTokens:         profile.MaxTokens,
			MaxOutputTokens:   profile.MaxOutputTokens,
			Temperature:       profile.Temperature,
			Reasoning:         reasoning,
		})
	default:
		return nil, fmt.Errorf("registry: unknown API %q for provider %q", api, p.Name)
	}
}
