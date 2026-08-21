package registry

import (
	"net/http"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/model/openai"
)

func TestNewCatalogPreservesOrder(t *testing.T) {
	cfg := Config{
		Providers: []ProviderConfig{
			{
				Name: "first", BaseURL: "https://first.example", API: "chat_completions",
				Profiles: []Profile{{Name: "a"}, {Name: "b"}},
			},
			{
				Name: "second", BaseURL: "https://second.example", API: "chat_completions",
				Profiles: []Profile{{Name: "c"}},
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
	if len(providers[0].Profiles) != 2 || providers[0].Profiles[0].Name != "a" || providers[0].Profiles[1].Name != "b" {
		t.Fatalf("profiles = %#v", providers[0].Profiles)
	}
}

func TestNewCatalogFillsDefaultModelNames(t *testing.T) {
	cat, err := NewCatalog(Config{Providers: []ProviderConfig{{
		Name: "p", BaseURL: "https://example.test", API: "chat_completions",
		Profiles: []Profile{{Name: "m"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.Providers()[0].Profiles[0].Model; got != "m" {
		t.Fatalf("model = %q, want %q", got, "m")
	}
}

func TestNewCatalogRejectsEmptyProvidersAndProviderWithoutModels(t *testing.T) {
	if _, err := NewCatalog(Config{}); err == nil {
		t.Fatal("expected error for empty providers")
	}
	if _, err := NewCatalog(Config{Providers: []ProviderConfig{{Name: "p"}}}); err == nil {
		t.Fatal("expected error for provider without profiles")
	}
}

func TestCapabilitiesRoundTrip(t *testing.T) {
	cfg := Config{Providers: []ProviderConfig{{
		Name: "p", BaseURL: "https://example.test", API: "chat_completions",
		Profiles: []Profile{{Name: "m", Capabilities: []dora.Capability{dora.CapabilityImageInput}}},
	}}}
	cat, err := NewCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := cat.Providers()[0].Profiles[0].Capabilities
	if len(got) != 1 || got[0] != dora.CapabilityImageInput {
		t.Fatalf("capabilities = %#v", got)
	}
}

func TestConstructChatCompletions(t *testing.T) {
	model, err := Construct(
		ProviderConfig{Name: "openai", BaseURL: "https://example.test/v1", API: "chat_completions", HTTPClient: &http.Client{}},
		Profile{Name: "m", Model: "test-model"},
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
		Profile{Name: "m", Model: "test-model"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if model == nil {
		t.Fatal("Construct returned nil model")
	}
}

// TestConstructPreserveThinking verifies the constructed chat adapter
// surfaces the profile-level PreserveThinking flag, and that the default
// (nil) leaves it disabled.
func TestConstructPreserveThinking(t *testing.T) {
	trueVal := true
	t.Run("enabled when profile sets it", func(t *testing.T) {
		model, err := Construct(
			ProviderConfig{Name: "deepseek", BaseURL: "https://example.test/v1", API: "chat_completions", HTTPClient: &http.Client{}},
			Profile{Name: "m", Model: "test-model", PreserveThinking: &trueVal},
		)
		if err != nil {
			t.Fatal(err)
		}
		oc, ok := model.(*openai.Client)
		if !ok {
			t.Fatalf("model type = %T, want *openai.Client", model)
		}
		if !oc.PreserveThinking() {
			t.Fatal("deepseek construct result must preserve thinking")
		}
	})
	t.Run("disabled by default", func(t *testing.T) {
		model, err := Construct(
			ProviderConfig{Name: "trust", BaseURL: "https://example.test/v1", API: "chat_completions", HTTPClient: &http.Client{}},
			Profile{Name: "m", Model: "test-model"},
		)
		if err != nil {
			t.Fatal(err)
		}
		oc, ok := model.(*openai.Client)
		if !ok {
			t.Fatalf("model type = %T, want *openai.Client", model)
		}
		if oc.PreserveThinking() {
			t.Fatal("default construct result must not preserve thinking")
		}
	})
}

func TestConstructUnknownAPI(t *testing.T) {
	if _, err := Construct(
		ProviderConfig{Name: "openai", BaseURL: "https://example.test/v1", API: "bogus"},
		Profile{Name: "m", Model: "test-model"},
	); err == nil {
		t.Fatal("expected error for unknown API")
	}
}
