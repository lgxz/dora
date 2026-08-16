package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultBuildsBuiltinCatalogWithProviderKey(t *testing.T) {
	clearBuiltinAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "secret")
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers = %#v", cfg.Providers)
	}
	p := providerByName(t, cfg, "deepseek")
	if p.BaseURL != "https://api.deepseek.com" || p.APIKey != "secret" {
		t.Fatalf("deepseek = %#v", p)
	}
	m := modelByName(t, p, "deepseek-v4-flash")
	if m.MaxTokens == nil || *m.MaxTokens != 32768 ||
		m.ContextWindow == nil || *m.ContextWindow != 1<<20 {
		t.Fatalf("models = %#v", p.Models)
	}
	if cfg.Client.Provider != "" || cfg.Client.Profile != "" {
		t.Fatalf("selector = %#v", cfg.Client)
	}
	if providerByName(t, cfg, "trust").APIKey != "" {
		t.Fatal("DeepSeek key leaked into Trust provider")
	}
}

func TestDefaultDORAModelSelectsBuiltinProfile(t *testing.T) {
	clearBuiltinAPIKeys(t)
	t.Setenv("DORA_MODEL", "trust/deepseek-v4-flash")
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Client.Provider != "trust" || cfg.Client.Profile != "deepseek-v4-flash" {
		t.Fatalf("selector = %#v", cfg.Client)
	}
}

func TestLoadDORAModelOverridesClient(t *testing.T) {
	clearBuiltinAPIKeys(t)
	path := writeConfig(t, `
providers:
  - name: custom
    base_url: https://custom.example/v1
    models:
      - name: default
      - name: team/fast
client:
  provider: custom
  profile: default
`)
	t.Setenv("DORA_MODEL", "custom/team/fast")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Client.Provider != "custom" || cfg.Client.Profile != "team/fast" {
		t.Fatalf("selector = %#v", cfg.Client)
	}
}

func TestLoadRejectsInvalidDORAModel(t *testing.T) {
	for _, value := range []string{"trust", "/auto", "trust/"} {
		t.Run(value, func(t *testing.T) {
			clearBuiltinAPIKeys(t)
			t.Setenv("DORA_MODEL", value)
			_, err := Default()
			if err == nil || !strings.Contains(err.Error(), "provider/profile") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadBuiltinProviderFillsConnectionDefaults(t *testing.T) {
	clearBuiltinAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-secret")
	cfg, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    models:
      - name: custom-deepseek
client:
  provider: deepseek
  profile: custom-deepseek
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %#v", cfg.Providers)
	}
	p := cfg.Providers[0]
	if p.BaseURL != "https://api.deepseek.com" || p.APIKey != "env-secret" || p.API != "chat_completions" {
		t.Fatalf("provider = %#v", p)
	}
	if cfg.Client.Provider != "deepseek" || cfg.Client.Profile != "custom-deepseek" {
		t.Fatalf("selector = %#v", cfg.Client)
	}
}

func TestLoadExplicitProviderValuesOverrideBuiltinDefaults(t *testing.T) {
	clearBuiltinAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	cfg, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    base_url: https://gateway.example/v1
    models:
      - name: custom-model
`))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Providers[0]
	if p.BaseURL != "https://gateway.example/v1" || p.APIKey != "deepseek-secret" {
		t.Fatalf("provider = %#v", p)
	}
}

func TestLoadCustomProviderRequiresBaseURL(t *testing.T) {
	_, err := Load(writeConfig(t, `
providers:
  - name: custom
    models:
      - name: model
`))
	if err == nil || !strings.Contains(err.Error(), "base_url cannot be empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadProcessEnvironmentOverridesConfigEnvironment(t *testing.T) {
	t.Setenv("CUSTOM_API_KEY", "env-secret")
	cfg, err := Load(writeConfig(t, `
providers:
  - name: custom
    base_url: https://custom.example/v1
    models:
      - name: model
env:
  CUSTOM_API_KEY: config-secret
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].APIKey != "env-secret" {
		t.Fatalf("api key = %q", cfg.Providers[0].APIKey)
	}
}

func TestLoadConfigEnvironmentSuppliesBuiltinProviderKeys(t *testing.T) {
	clearBuiltinAPIKeys(t)
	cfg, err := Load(writeConfig(t, `
env:
  DEEPSEEK_API_KEY: deepseek-secret
  TRUST_API_KEY: trust-secret
`))
	if err != nil {
		t.Fatal(err)
	}
	if providerByName(t, cfg, "deepseek").APIKey != "deepseek-secret" || providerByName(t, cfg, "trust").APIKey != "trust-secret" {
		t.Fatalf("providers = %#v", cfg.Providers)
	}
}

func TestLoadDerivesDistinctProviderAPIKeyEnvironments(t *testing.T) {
	clearBuiltinAPIKeys(t)
	t.Setenv("FOO_BAR_API_KEY", "foo-secret")
	t.Setenv("BAZ_API_KEY", "baz-secret")
	cfg, err := Load(writeConfig(t, `
providers:
  - name: foo-bar
    base_url: https://foo.example/v1
    models: [{name: model}]
  - name: baz
    base_url: https://baz.example/v1
    models: [{name: model}]
client:
  provider: foo-bar
  profile: model
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].APIKey != "foo-secret" || cfg.Providers[1].APIKey != "baz-secret" {
		t.Fatalf("providers = %#v", cfg.Providers)
	}
}

func TestLoadRejectsProviderAPIKeyEnvironmentCollision(t *testing.T) {
	_, err := Load(writeConfig(t, `
providers:
  - name: foo-bar
    base_url: https://foo.example/v1
    models: [{name: model}]
  - name: foo_bar
    base_url: https://bar.example/v1
    models: [{name: model}]
`))
	if err == nil || !strings.Contains(err.Error(), "FOO_BAR_API_KEY") || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsAPIKeyEnv(t *testing.T) {
	_, err := Load(writeConfig(t, `
providers:
  - name: custom
    base_url: https://custom.example/v1
    api_key_env: CUSTOM_KEY
    models:
      - name: model
`))
	if err == nil || !strings.Contains(err.Error(), "field api_key_env not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsProviderAPIKey(t *testing.T) {
	_, err := Load(writeConfig(t, `
providers:
  - name: custom
    base_url: https://custom.example/v1
    api_key: secret
    models:
      - name: model
`))
	if err == nil || !strings.Contains(err.Error(), "field api_key not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsUnknownConfigEnvironment(t *testing.T) {
	_, err := Load(writeConfig(t, `
env:
  OPENAI_API_KEY: secret
`))
	if err == nil || !strings.Contains(err.Error(), "does not match any configured provider") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsLegacyFlatModelFields(t *testing.T) {
	t.Run("top-level model", func(t *testing.T) {
		_, err := Load(writeConfig(t, `
model:
  provider: openai
  name: gpt-5
`))
		if err == nil || !strings.Contains(err.Error(), "field model not found") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("client connection field", func(t *testing.T) {
		_, err := Load(writeConfig(t, `
client:
  provider: openai
  base_url: https://api.openai.com/v1
`))
		if err == nil || !strings.Contains(err.Error(), "field base_url not found") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("agent context window", func(t *testing.T) {
		_, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    models: [{name: model}]
agent:
  context_window: 1048576
`))
		if err == nil || !strings.Contains(err.Error(), "field context_window not found") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestLoadAppliesModelDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    models:
      - name: flash
      - name: reasoner
        model: flash
        max_tokens: 0
        context_window: 2048
`))
	if err != nil {
		t.Fatal(err)
	}
	models := cfg.Providers[0].Models
	if models[0].Model != "flash" || models[0].MaxTokens == nil || *models[0].MaxTokens != 32768 ||
		models[0].ContextWindow == nil || *models[0].ContextWindow != 1<<20 || models[0].Thinking != nil {
		t.Fatalf("default model = %#v", models[0])
	}
	if models[1].Model != "flash" || models[1].MaxTokens == nil || *models[1].MaxTokens != 0 ||
		models[1].ContextWindow == nil || *models[1].ContextWindow != 2048 {
		t.Fatalf("explicit values = %#v", models[1])
	}
}

func TestLoadRejectsDuplicateProviderAndModelNames(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		_, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    models: [{name: one}]
  - name: deepseek
    models: [{name: two}]
`))
		if err == nil || !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("model", func(t *testing.T) {
		_, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    models:
      - name: same
      - name: same
`))
		if err == nil || !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestLoadRejectsInvalidProviderAndModelValues(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"empty providers", "providers: []\n", "providers cannot be empty"},
		{"invalid api", "providers:\n  - name: deepseek\n    api: bogus\n    models: [{name: model}]\n", "must be"},
		{"negative timeout", "providers:\n  - name: deepseek\n    timeout_seconds: -1\n    models: [{name: model}]\n", "cannot be negative"},
		{"negative max tokens", "providers:\n  - name: deepseek\n    models: [{name: model, max_tokens: -1}]\n", "cannot be negative"},
		{"zero context window", "providers:\n  - name: deepseek\n    models: [{name: model, context_window: 0}]\n", "context_window must be positive"},
		{"negative context window", "providers:\n  - name: deepseek\n    models: [{name: model, context_window: -1}]\n", "context_window must be positive"},
		{"temperature", "providers:\n  - name: deepseek\n    models: [{name: model, temperature: 3}]\n", "within [0, 2]"},
		{"thinking", "providers:\n  - name: deepseek\n    models: [{name: model, thinking: turbo}]\n", "thinking must be one of"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	_, err := Load(writeConfig(t, "providers: []\nunknown: true\n"))
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("unknown field error = %v", err)
	}
	_, err = Load(writeConfig(t, "providers: []\n---\nproviders: []\n"))
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("multiple document error = %v", err)
	}
}

func TestLoadValidatesSharedSettings(t *testing.T) {
	_, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    models: [{name: model}]
agent:
  max_rounds: -1
`))
	if err == nil || !strings.Contains(err.Error(), "agent.max_rounds cannot be negative") {
		t.Fatalf("error = %v", err)
	}
}

func providerByName(t *testing.T, cfg Config, name string) Provider {
	t.Helper()
	for _, provider := range cfg.Providers {
		if provider.Name == name {
			return provider
		}
	}
	t.Fatalf("provider %q not found", name)
	return Provider{}
}

func modelByName(t *testing.T, provider Provider, name string) ModelSpec {
	t.Helper()
	for _, model := range provider.Models {
		if model.Name == name {
			return model
		}
	}
	t.Fatalf("model profile %q not found under provider %q", name, provider.Name)
	return ModelSpec{}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearBuiltinAPIKeys(t *testing.T) {
	t.Helper()
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("TRUST_API_KEY", "")
	t.Setenv("DORA_MODEL", "")
}
