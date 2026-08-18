package registry

import (
	"net/http"
	"testing"

	"github.com/lgxz/dora"
)

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

func TestNewCatalogFillsDefaultModelNames(t *testing.T) {
	cat, err := NewCatalog(Config{Providers: []ProviderConfig{{
		Name: "p", BaseURL: "https://example.test", API: "chat_completions",
		Models: []ModelConfig{{Name: "m"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Providers()[0].Models[0].Model; got != "m" {
		t.Fatalf("model = %q, want %q", got, "m")
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
