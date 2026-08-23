package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgxz/dora"
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
		m.ContextWindow == nil || *m.ContextWindow != 1000000 {
		t.Fatalf("profiles = %#v", p.Profiles)
	}
	if cfg.Policy.Text.Provider != "" || cfg.Policy.Text.Profile != "" {
		t.Fatalf("selector = %#v", cfg.Policy)
	}
	if providerByName(t, cfg, "trust").APIKey != "" {
		t.Fatal("DeepSeek key leaked into Trust provider")
	}
}

func TestLoadPolicyFromYAML(t *testing.T) {
	clearBuiltinAPIKeys(t)
	cfg, err := Load(writeConfig(t, `
providers:
  - name: custom
    base_url: https://custom.example/v1
    profiles:
      - name: default
      - name: team/fast
policy:
  text:
    provider: custom
    profile: default
  image:
    profile: team/fast
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policy.Text.Provider != "custom" || cfg.Policy.Text.Profile != "default" {
		t.Fatalf("text policy = %#v", cfg.Policy.Text)
	}
	if cfg.Policy.Image.Profile != "team/fast" {
		t.Fatalf("image policy = %#v", cfg.Policy.Image)
	}
}

func TestLoadPolicyEnvOverride(t *testing.T) {
	clearBuiltinAPIKeys(t)
	t.Setenv("DORA_POLICY_TEXT_PROVIDER", "custom")
	t.Setenv("DORA_POLICY_IMAGE_PROFILE", "team/fast")
	cfg, err := Load(writeConfig(t, `
providers:
  - name: custom
    base_url: https://custom.example/v1
    profiles:
      - name: default
      - name: team/fast
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policy.Text.Provider != "custom" {
		t.Fatalf("text policy = %#v", cfg.Policy.Text)
	}
	if cfg.Policy.Image.Profile != "team/fast" {
		t.Fatalf("image policy = %#v", cfg.Policy.Image)
	}
}

func TestLoadBuiltinProviderFillsConnectionDefaults(t *testing.T) {
	clearBuiltinAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "env-secret")
	cfg, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles:
      - name: custom-deepseek
policy:
  text:
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
	if cfg.Policy.Text.Provider != "deepseek" || cfg.Policy.Text.Profile != "custom-deepseek" {
		t.Fatalf("selector = %#v", cfg.Policy.Text)
	}
}

func TestLoadExplicitProviderValuesOverrideBuiltinDefaults(t *testing.T) {
	clearBuiltinAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	cfg, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    base_url: https://gateway.example/v1
    profiles:
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
    profiles:
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
    profiles:
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
    profiles: [{name: model}]
  - name: baz
    base_url: https://baz.example/v1
    profiles: [{name: model}]
policy:
  text:
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
    profiles: [{name: model}]
  - name: foo_bar
    base_url: https://bar.example/v1
    profiles: [{name: model}]
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
    profiles:
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
    profiles:
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

func TestLoadCapabilitiesRoundTrip(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles:
      - name: flash
        capabilities: [text, image_input]
`))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Providers[0].Profiles[0].Capabilities
	if len(got) != 2 || got[0] != dora.CapabilityText || got[1] != dora.CapabilityImageInput {
		t.Fatalf("capabilities = %#v", got)
	}
}

func TestLoadRejectsUnknownCapability(t *testing.T) {
	_, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles:
      - name: flash
        capabilities: [text, bogus]
`))
	if err == nil || !strings.Contains(err.Error(), "unknown capability") {
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
		if err == nil || !strings.Contains(err.Error(), "field client not found") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("vision field", func(t *testing.T) {
		_, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles: [{name: model, vision: true}]
`))
		if err == nil || !strings.Contains(err.Error(), "field vision not found") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("agent context window", func(t *testing.T) {
		_, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles: [{name: model}]
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
    profiles:
      - name: flash
      - name: reasoner
        model: flash
        max_tokens: 0
        context_window: 2048
`))
	if err != nil {
		t.Fatal(err)
	}
	profiles := cfg.Providers[0].Profiles
	if profiles[0].Model != "flash" || profiles[0].MaxTokens == nil || *profiles[0].MaxTokens != 32768 ||
		profiles[0].ContextWindow == nil || *profiles[0].ContextWindow != 1<<20 || profiles[0].Thinking != nil {
		t.Fatalf("default model = %#v", profiles[0])
	}
	if profiles[1].Model != "flash" || profiles[1].MaxTokens == nil || *profiles[1].MaxTokens != 0 ||
		profiles[1].ContextWindow == nil || *profiles[1].ContextWindow != 2048 {
		t.Fatalf("explicit values = %#v", profiles[1])
	}
}

func TestLoadRejectsDuplicateProviderAndModelNames(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		_, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles: [{name: one}]
  - name: deepseek
    profiles: [{name: two}]
`))
		if err == nil || !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("model", func(t *testing.T) {
		_, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles:
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
		{"invalid api", "providers:\n  - name: deepseek\n    api: bogus\n    profiles: [{name: model}]\n", "must be"},
		{"negative timeout", "providers:\n  - name: deepseek\n    timeout_seconds: -1\n    profiles: [{name: model}]\n", "cannot be negative"},
		{"negative max tokens", "providers:\n  - name: deepseek\n    profiles: [{name: model, max_tokens: -1}]\n", "cannot be negative"},
		{"zero context window", "providers:\n  - name: deepseek\n    profiles: [{name: model, context_window: 0}]\n", "context_window must be positive"},
		{"negative context window", "providers:\n  - name: deepseek\n    profiles: [{name: model, context_window: -1}]\n", "context_window must be positive"},
		{"temperature", "providers:\n  - name: deepseek\n    profiles: [{name: model, temperature: 3}]\n", "within [0, 2]"},
		{"thinking", "providers:\n  - name: deepseek\n    profiles: [{name: model, thinking: turbo}]\n", "thinking must be one of"},
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
    profiles: [{name: model}]
agent:
  max_rounds: -1
`))
	if err == nil || !strings.Contains(err.Error(), "agent.max_rounds cannot be negative") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadAgentSystemPrompt(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles: [{name: model}]
agent:
  system_prompt: "You are a pirate."
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.SystemPrompt != "You are a pirate." {
		t.Fatalf("cfg.Agent.SystemPrompt = %q, want the configured prompt", cfg.Agent.SystemPrompt)
	}
}

func TestLoadTaskEnabledOverride(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles: [{name: model}]
tools:
  task:
    enabled: false
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools.Task.Enabled == nil || *cfg.Tools.Task.Enabled {
		t.Fatalf("tools.task.enabled = %#v, want false", cfg.Tools.Task.Enabled)
	}
}

func TestLoadEventsConfig(t *testing.T) {
	clearBuiltinAPIKeys(t)
	cfg, err := Load(writeConfig(t, `
providers:
  - name: deepseek
    profiles: [{name: model}]
events:
  enabled: true
  transports:
    memberlist:
      bind: 127.0.0.1:4444
      name: dora
      join:
        - 10.0.0.2:8848
        - 10.0.0.3:8848
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Events.Enabled {
		t.Fatal("events.enabled should be true")
	}
	ml := cfg.Events.Transports.Memberlist
	if ml.Bind != "127.0.0.1:4444" || ml.Name != "dora" {
		t.Fatalf("memberlist = %+v", ml)
	}
	if len(ml.Join) != 2 || ml.Join[0] != "10.0.0.2:8848" || ml.Join[1] != "10.0.0.3:8848" {
		t.Fatalf("join = %+v", ml.Join)
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

func modelByName(t *testing.T, provider Provider, name string) ProfileSpec {
	t.Helper()
	for _, profile := range provider.Profiles {
		if profile.Name == name {
			return profile
		}
	}
	t.Fatalf("model profile %q not found under provider %q", name, provider.Name)
	return ProfileSpec{}
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
	t.Setenv("DORA_POLICY_TEXT_PROVIDER", "")
	t.Setenv("DORA_POLICY_TEXT_PROFILE", "")
	t.Setenv("DORA_POLICY_IMAGE_PROVIDER", "")
	t.Setenv("DORA_POLICY_IMAGE_PROFILE", "")
}
