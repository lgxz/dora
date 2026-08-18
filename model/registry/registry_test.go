package registry

import (
	"net/http"
	"strings"
	"testing"

	"github.com/lgxz/dora"
)

func TestNewSelectsSoleProvider(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{{
			Name:    "openai",
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "secret",
			API:     "chat_completions",
			Models:  []ModelConfig{{Name: "gpt-5"}},
		}},
	}
	reg, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sel := reg.Selection()
	if sel.Provider != "openai" || sel.Profile != "gpt-5" || sel.Model != "gpt-5" || sel.API != "chat_completions" {
		t.Fatalf("selection = %#v", sel)
	}
}

func TestNewRequiresExplicitProviderWhenMultipleConfigured(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{
			{Name: "openai", BaseURL: "https://api.openai.com/v1", API: "chat_completions", Models: []ModelConfig{{Name: "gpt-5"}}},
			{Name: "deepseek", BaseURL: "https://api.deepseek.com", API: "chat_completions", Models: []ModelConfig{{Name: "deepseek-v4-flash"}}},
		},
	}
	_, err := New(cfg)
	if err == nil {
		t.Fatal("expected error for multiple providers")
	}
}

func TestNewSelectsOnlyProviderWithAPIKey(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{
			{Name: "openai", BaseURL: "https://api.openai.com/v1", API: "chat_completions", Models: []ModelConfig{{Name: "gpt-5"}}},
			{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKey: "ds-secret", API: "chat_completions", Models: []ModelConfig{{Name: "deepseek-v4-flash"}}},
		},
	}
	reg, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Selection().Provider != "deepseek" {
		t.Fatalf("selection = %#v", reg.Selection())
	}
}

func TestNewRejectsMultipleProvidersWithAPIKeys(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{
			{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "oa-secret", API: "chat_completions", Models: []ModelConfig{{Name: "gpt-5"}}},
			{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKey: "ds-secret", API: "chat_completions", Models: []ModelConfig{{Name: "deepseek-v4-flash"}}},
		},
	}
	_, err := New(cfg)
	if err == nil || !strings.Contains(err.Error(), "multiple providers") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewSelectsExplicitProvider(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{
			{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "oa-secret", API: "chat_completions", Models: []ModelConfig{{Name: "gpt-5"}}},
			{Name: "deepseek", BaseURL: "https://api.deepseek.com", APIKey: "ds-secret", API: "chat_completions", Models: []ModelConfig{{Name: "deepseek-v4-flash"}}},
		},
		SelectedProvider: "openai",
	}
	reg, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Selection().Provider != "openai" {
		t.Fatalf("provider = %q", reg.Selection().Provider)
	}
}

func TestNewSelectsExplicitProfileWithRepeatedModel(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{{
			Name:    "deepseek",
			BaseURL: "https://api.deepseek.com",
			APIKey:  "secret",
			API:     "chat_completions",
			Models: []ModelConfig{
				{Name: "fast", Model: "deepseek-chat", ContextWindow: intPtr(1000)},
				{Name: "reasoning", Model: "deepseek-chat", Thinking: strPtr("high"), ContextWindow: intPtr(2000)},
			},
		}},
		SelectedProfile: "reasoning",
	}
	reg, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Selection().Profile != "reasoning" || reg.Selection().Model != "deepseek-chat" ||
		reg.Selection().ContextWindow == nil || *reg.Selection().ContextWindow != 2000 {
		t.Fatalf("selection = %#v", reg.Selection())
	}
}

func TestNewSelectsFirstProfileByDefault(t *testing.T) {
	reg, err := New(Config{Providers: []ProviderConfig{{
		Name:    "deepseek",
		BaseURL: "https://api.deepseek.com",
		API:     "chat_completions",
		Models: []ModelConfig{
			{Name: "flash", Model: "deepseek-chat"},
			{Name: "pro", Model: "deepseek-reasoner"},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if reg.Selection().Profile != "flash" {
		t.Fatalf("selection = %#v", reg.Selection())
	}
}

func TestModelOverridesProviderAPI(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{{
			Name:    "openai",
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "secret",
			API:     "chat_completions",
			Models:  []ModelConfig{{Name: "gpt-5", API: "responses"}},
		}},
	}
	reg, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reg.Selection().API != "responses" {
		t.Fatalf("api = %q, want responses", reg.Selection().API)
	}
}

func TestSetThinkingOverride(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{{
			Name:    "openai",
			BaseURL: "https://api.openai.com/v1",
			APIKey:  "secret",
			API:     "chat_completions",
			Models:  []ModelConfig{{Name: "gpt-5"}},
		}},
	}
	reg, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reg.SetThinking(strPtr("low"))
	sel := reg.Selection()
	if sel.Thinking == nil || *sel.Thinking != "low" {
		t.Fatalf("thinking = %#v", sel.Thinking)
	}
}

func TestModelInstantiatesChatCompletions(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{{
			Name:       "openai",
			BaseURL:    "https://example.test/v1",
			APIKey:     "secret",
			API:        "chat_completions",
			HTTPClient: &http.Client{},
			Models:     []ModelConfig{{Name: "test-model"}},
		}},
	}
	reg, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Model(); err != nil {
		t.Fatalf("Model() error: %v", err)
	}
}

func TestModelInstantiatesResponses(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{{
			Name:       "openai",
			BaseURL:    "https://example.test/v1",
			APIKey:     "secret",
			API:        "responses",
			HTTPClient: &http.Client{},
			Models:     []ModelConfig{{Name: "test-model"}},
		}},
	}
	reg, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Model(); err != nil {
		t.Fatalf("Model() error: %v", err)
	}
}

func TestNewRejectsEmptyProviders(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("expected error for empty providers")
	}
}

func TestNewRejectsProviderWithoutModels(t *testing.T) {
	_, err := New(Config{
		Providers: []ProviderConfig{{Name: "openai", BaseURL: "https://example.test", APIKey: "k", API: "chat_completions"}},
	})
	if err == nil {
		t.Fatal("expected error for provider without models")
	}
}

func TestNewCatalogPreservesOrder(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{
			{
				Name: "first", BaseURL: "https://first.example", API: "chat_completions",
				Models: []ModelConfig{{Name: "a"}, {Name: "b"}},
			},
			{
				Name: "second", BaseURL: "https://second.example", API: "chat_completions",
				Models: []ModelConfig{{Name: "c"}},
			},
		},
	}
	cat, err := NewCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	providers := cat.Providers()
	if len(providers) != 2 || providers[0].Name != "first" || providers[1].Name != "second" {
		t.Fatalf("providers = %#v", providers)
	}
	if len(providers[0].Models) != 2 || providers[0].Models[0].Name != "a" || providers[0].Models[1].Name != "b" {
		t.Fatalf("models = %#v", providers[0].Models)
	}
}

func TestNewCatalogRejectsEmptyProvidersAndProviderWithoutModels(t *testing.T) {
	if _, err := NewCatalog(Config{}); err == nil {
		t.Fatal("expected error for empty providers")
	}
	if _, err := NewCatalog(Config{Providers: []ProviderConfig{{Name: "p"}}}); err == nil {
		t.Fatal("expected error for provider without models")
	}
}

func TestCapabilitiesRoundTrip(t *testing.T) {
	cfg := Config{Providers: []ProviderConfig{{
		Name: "p", BaseURL: "https://example.test", API: "chat_completions",
		Models: []ModelConfig{{Name: "m", Capabilities: []dora.Capability{dora.CapabilityImageInput}}},
	}}}
	cat, err := NewCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := cat.Providers()[0].Models[0].Capabilities
	if len(got) != 1 || got[0] != dora.CapabilityImageInput {
		t.Fatalf("capabilities = %#v", got)
	}
}

func TestConstructChatCompletions(t *testing.T) {
	model, err := Construct(
		ProviderConfig{Name: "openai", BaseURL: "https://example.test/v1", API: "chat_completions", HTTPClient: &http.Client{}},
		ModelConfig{Name: "m", Model: "test-model"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("Construct returned nil model")
	}
}

func TestConstructResponses(t *testing.T) {
	model, err := Construct(
		ProviderConfig{Name: "openai", BaseURL: "https://example.test/v1", API: "responses", HTTPClient: &http.Client{}},
		ModelConfig{Name: "m", Model: "test-model"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("Construct returned nil model")
	}
}

func TestConstructUnknownAPI(t *testing.T) {
	if _, err := Construct(
		ProviderConfig{Name: "openai", BaseURL: "https://example.test/v1", API: "bogus"},
		ModelConfig{Name: "m", Model: "test-model"},
	); err == nil {
		t.Fatal("expected error for unknown API")
	}
}

func strPtr(v string) *string { return &v }

func intPtr(v int) *int { return &v }
