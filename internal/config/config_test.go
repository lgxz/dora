package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("OPENAI_API_KEY", "test-secret")
	os.Exit(m.Run())
}

func TestDefaultUsesDeepSeek(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")

	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "deepseek" || cfg.Model.API != "chat_completions" ||
		cfg.Model.Name != "deepseek-v4-flash" || cfg.Model.BaseURL != "https://api.deepseek.com" ||
		cfg.Model.APIKey != "deepseek-secret" {
		t.Fatalf("model = %#v", cfg.Model)
	}
	if cfg.Tools.Bash.Enabled != nil || cfg.Tools.PowerShell.Enabled != nil {
		t.Fatalf("tool defaults are not automatic: %#v", cfg.Tools)
	}
}

func TestLoadUsesDeepSeekWhenProviderIsOmitted(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	path := writeConfig(t, "agent:\n  max_rounds: 32\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "deepseek" || cfg.Model.Name != "deepseek-v4-flash" || cfg.Agent.MaxRounds != 32 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestLoadSelectsProviderFromEnvironment(t *testing.T) {
	for _, test := range []struct {
		name     string
		env      string
		key      string
		provider string
		model    string
		baseURL  string
	}{
		{
			name:     "openai",
			env:      "OPENAI_API_KEY",
			key:      "openai-secret",
			provider: "openai",
			model:    "gpt-5",
			baseURL:  "https://api.openai.com/v1",
		},
		{
			name:     "trust",
			env:      "TRUST_API_KEY",
			key:      "trust-secret",
			provider: "trust",
			model:    "auto",
			baseURL:  "https://api.trustoken.cn/v1",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearPresetAPIKeys(t)
			t.Setenv(test.env, test.key)
			path := writeConfig(t, "agent:\n  max_rounds: 32\n")

			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Model.Provider != test.provider || cfg.Model.Name != test.model ||
				cfg.Model.BaseURL != test.baseURL || cfg.Model.APIKey != test.key {
				t.Fatalf("model = %#v", cfg.Model)
			}
		})
	}
}

func TestLoadEnvironmentAPIKeyWinsOverConfigLiteral(t *testing.T) {
	// Repro: env DEEPSEEK_API_KEY set, config provider: trust with a literal
	// api_key. selectProvider() overrides the provider to deepseek and the
	// resolved provider's env key takes precedence over the config literal.
	clearPresetAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	path := writeConfig(t, "model:\n  provider: trust\n  api_key: trust-literal\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "deepseek" || cfg.Model.APIKey != "deepseek-secret" {
		t.Fatalf("model = %#v, want provider deepseek with env API key", cfg.Model)
	}
}

func TestLoadEnvironmentOverridesConfigProvider(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("TRUST_API_KEY", "trust-secret")
	path := writeConfig(t, "model:\n  provider: deepseek\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "trust" || cfg.Model.Name != "auto" ||
		cfg.Model.BaseURL != "https://api.trustoken.cn/v1" || cfg.Model.APIKey != "trust-secret" {
		t.Fatalf("model = %#v", cfg.Model)
	}
}

func TestLoadRejectsAmbiguousProviderEnvironment(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	t.Setenv("TRUST_API_KEY", "trust-secret")
	path := writeConfig(t, "agent:\n  max_rounds: 32\n")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "model.provider is ambiguous") ||
		!strings.Contains(err.Error(), "deepseek, trust") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsMissingProviderWithoutEnvironment(t *testing.T) {
	clearPresetAPIKeys(t)
	path := writeConfig(t, "model:\n  api_key: literal-secret\n")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "no model provider configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadEnvironmentOverridesModelForResolvedProvider(t *testing.T) {
	for _, test := range []struct {
		name     string
		env      string
		key      string
		modelEnv string
		baseURL  string
	}{
		{name: "openai", env: "OPENAI_API_KEY", key: "openai-secret", modelEnv: "OPENAI_MODEL", baseURL: "https://api.openai.com/v1"},
		{name: "deepseek", env: "DEEPSEEK_API_KEY", key: "deepseek-secret", modelEnv: "DEEPSEEK_MODEL", baseURL: "https://api.deepseek.com"},
		{name: "trust", env: "TRUST_API_KEY", key: "trust-secret", modelEnv: "TRUST_MODEL", baseURL: "https://api.trustoken.cn/v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearPresetAPIKeys(t)
			t.Setenv(test.env, test.key)
			t.Setenv(test.modelEnv, "env-model")
			path := writeConfig(t, "model:\n  provider: "+test.name+"\n  name: config-model\n")

			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Model.Name != "env-model" || cfg.Model.BaseURL != test.baseURL {
				t.Fatalf("model = %#v, want name %q baseURL %q", cfg.Model, "env-model", test.baseURL)
			}
		})
	}
}

func TestLoadEnvironmentOverridesBaseURLForResolvedProvider(t *testing.T) {
	for _, test := range []struct {
		name       string
		env        string
		key        string
		baseURLEnv string
		baseURL    string
	}{
		{name: "openai", env: "OPENAI_API_KEY", key: "openai-secret", baseURLEnv: "OPENAI_BASE_URL", baseURL: "https://api.openai.com/v1"},
		{name: "deepseek", env: "DEEPSEEK_API_KEY", key: "deepseek-secret", baseURLEnv: "DEEPSEEK_BASE_URL", baseURL: "https://api.deepseek.com"},
		{name: "trust", env: "TRUST_API_KEY", key: "trust-secret", baseURLEnv: "TRUST_BASE_URL", baseURL: "https://api.trustoken.cn/v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearPresetAPIKeys(t)
			t.Setenv(test.env, test.key)
			t.Setenv(test.baseURLEnv, "https://env.example.com/v1")
			path := writeConfig(t, "model:\n  provider: "+test.name+"\n  name: "+test.baseURL+"\n  base_url: http://localhost\n")

			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Model.BaseURL != "https://env.example.com/v1" {
				t.Fatalf("model = %#v, want baseURL https://env.example.com/v1", cfg.Model)
			}
		})
	}
}

func TestLoadDoesNotApplyOtherProviderEnvironmentOverride(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	// Set the trust and openai MODEL/BASE_URL env vars; they must NOT affect
	// the resolved deepseek provider.
	t.Setenv("TRUST_MODEL", "trust-model")
	t.Setenv("TRUST_BASE_URL", "https://trust.example.com/v1")
	t.Setenv("OPENAI_MODEL", "openai-model")
	t.Setenv("OPENAI_BASE_URL", "https://openai.example.com/v1")
	path := writeConfig(t, "model:\n  provider: deepseek\n  name: config-model\n  base_url: http://localhost\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Name != "config-model" || cfg.Model.BaseURL != "http://localhost" {
		t.Fatalf("model = %#v, want config values preserved", cfg.Model)
	}
}

func TestLoadLeavesCommandToolDefaultsAutomatic(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools.Bash.Enabled != nil || cfg.Tools.PowerShell.Enabled != nil {
		t.Fatalf("tools = %#v, want automatic enablement", cfg.Tools)
	}
}

func TestLoadAllowsBashToBeDisabled(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
tools:
  bash:
    enabled: false
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools.Bash.Enabled == nil || *cfg.Tools.Bash.Enabled {
		t.Fatalf("bash enabled = %v, want explicit false", cfg.Tools.Bash.Enabled)
	}
}

func TestLoadAllowsBashToBeEnabled(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
tools:
  bash:
    enabled: true
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools.Bash.Enabled == nil || !*cfg.Tools.Bash.Enabled {
		t.Fatalf("bash enabled = %v, want explicit true", cfg.Tools.Bash.Enabled)
	}
}

func TestLoadAllowsPowerShellToBeDisabled(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
tools:
  powershell:
    enabled: false
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools.PowerShell.Enabled == nil || *cfg.Tools.PowerShell.Enabled {
		t.Fatalf("powershell enabled = %v, want explicit false", cfg.Tools.PowerShell.Enabled)
	}
}

func TestLoadRejectsRemovedToolWorkingDirectory(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
tools:
  bash:
    working_dir: /tmp
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "working_dir") {
		t.Fatalf("error = %v, want unknown working_dir", err)
	}
}

func TestLoadRejectsToolTimeoutAboveLimit(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
tools:
  powershell:
    timeout_seconds: 3601
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "cannot exceed 3600") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadResolvesEnvironmentAPIKey(t *testing.T) {
	t.Setenv("DORA_TEST_API_KEY", "secret")
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost:8080/v1
  api_key_env: DORA_TEST_API_KEY
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.APIKey != "secret" {
		t.Fatalf("api key = %q", cfg.Model.APIKey)
	}
}

func TestLoadAcceptsResponsesAPI(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  api: responses
  name: gpt-5
  base_url: https://api.openai.com/v1
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.API != "responses" {
		t.Fatalf("api = %q", cfg.Model.API)
	}
}

func TestLoadAppliesDeepSeekDefaults(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	path := writeConfig(t, "model:\n  provider: deepseek\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.API != "chat_completions" || cfg.Model.Name != "deepseek-v4-flash" ||
		cfg.Model.BaseURL != "https://api.deepseek.com" || cfg.Model.APIKey != "deepseek-secret" {
		t.Fatalf("model = %#v", cfg.Model)
	}
}

func TestLoadAppliesTrustDefaults(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("TRUST_API_KEY", "trust-secret")
	path := writeConfig(t, "model:\n  provider: trust\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.API != "chat_completions" || cfg.Model.Name != "auto" ||
		cfg.Model.BaseURL != "https://api.trustoken.cn/v1" || cfg.Model.APIKey != "trust-secret" {
		t.Fatalf("model = %#v", cfg.Model)
	}
}

func TestLoadAppliesOpenAIDefaults(t *testing.T) {
	path := writeConfig(t, "model:\n  provider: openai\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.API != "chat_completions" || cfg.Model.Name != "gpt-5" ||
		cfg.Model.BaseURL != "https://api.openai.com/v1" || cfg.Model.APIKey != "test-secret" {
		t.Fatalf("model = %#v", cfg.Model)
	}
}

func TestLoadAllowsExplicitlyDisabledAPIKeyEnvironment(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: local-model
  base_url: http://localhost:8080/v1
  api_key_env: ""
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.APIKey != "" || cfg.Model.APIKeyEnv == nil || *cfg.Model.APIKeyEnv != "" {
		t.Fatalf("model = %#v", cfg.Model)
	}
}

func TestLoadRejectsLegacyProvider(t *testing.T) {
	clearPresetAPIKeys(t)
	path := writeConfig(t, "model:\n  provider: openai-compatible\n")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `must be "openai", "deepseek", or "trust"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsUnknownAPI(t *testing.T) {
	path := writeConfig(t, "model:\n  provider: openai\n  api: completions\n")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `must be "chat_completions" or "responses"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadAcceptsSkillDirectories(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
skills:
  directories:
    - /opt/dora/skills
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Skills.Directories) != 1 || cfg.Skills.Directories[0] != "/opt/dora/skills" {
		t.Fatalf("directories = %#v", cfg.Skills.Directories)
	}
}

func TestLoadRejectsEmptySkillDirectory(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
skills:
  directories:
    - ""
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadAcceptsAgentModelCallLimit(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
agent:
  max_rounds: 96
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.MaxRounds != 96 {
		t.Fatalf("max rounds = %d", cfg.Agent.MaxRounds)
	}
}

func TestLoadRejectsNegativeAgentRoundLimit(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
agent:
  max_rounds: -1
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "cannot be negative") {
		t.Fatalf("error = %v", err)
	}
}

func TestDefaultModelTimeouts(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.TimeoutSeconds != 120 || cfg.Model.ConnectTimeoutSeconds != 10 || cfg.Model.StreamIdleTimeoutSeconds != 0 {
		t.Fatalf("model timeouts = %#v", cfg.Model)
	}
}

func TestLoadAcceptsModelTimeouts(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
  timeout_seconds: 60
  connect_timeout_seconds: 5
  stream_idle_timeout_seconds: 30
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.TimeoutSeconds != 60 || cfg.Model.ConnectTimeoutSeconds != 5 || cfg.Model.StreamIdleTimeoutSeconds != 30 {
		t.Fatalf("model timeouts = %#v", cfg.Model)
	}
}

func TestLoadRejectsNegativeModelTimeouts(t *testing.T) {
	for _, field := range []string{"timeout_seconds", "connect_timeout_seconds", "stream_idle_timeout_seconds"} {
		t.Run(field, func(t *testing.T) {
			path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n  "+field+": -1\n")
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "cannot be negative") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDefaultMaxTokensIs32768(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.MaxTokens == nil || *cfg.Model.MaxTokens != 32768 {
		t.Fatalf("max_tokens = %#v, want 32768", cfg.Model.MaxTokens)
	}
	if cfg.Model.Temperature != nil {
		t.Fatalf("temperature = %#v, want nil", cfg.Model.Temperature)
	}
}

func TestLoadMaxTokensWhenOmittedKeepsDefault(t *testing.T) {
	// A user config without the key must keep the 32768 default on the wire.
	clearPresetAPIKeys(t)
	t.Setenv("OPENAI_API_KEY", "test-secret")
	path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.MaxTokens == nil || *cfg.Model.MaxTokens != 32768 {
		t.Fatalf("max_tokens = %#v, want 32768", cfg.Model.MaxTokens)
	}
	if cfg.Model.Temperature != nil {
		t.Fatalf("temperature = %#v, want nil", cfg.Model.Temperature)
	}
}

func TestLoadOverridesMaxTokens(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("OPENAI_API_KEY", "test-secret")
	path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n  max_tokens: 4096\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.MaxTokens == nil || *cfg.Model.MaxTokens != 4096 {
		t.Fatalf("max_tokens = %#v, want 4096", cfg.Model.MaxTokens)
	}
}

func TestLoadKeepsExplicitZeroMaxTokens(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("OPENAI_API_KEY", "test-secret")
	path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n  max_tokens: 0\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.MaxTokens == nil || *cfg.Model.MaxTokens != 0 {
		t.Fatalf("max_tokens = %#v, want explicit 0", cfg.Model.MaxTokens)
	}
}

func TestLoadRejectsNegativeMaxTokens(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("OPENAI_API_KEY", "test-secret")
	path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n  max_tokens: -1\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "model.max_tokens cannot be negative") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadAcceptsTemperature(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("OPENAI_API_KEY", "test-secret")
	path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n  temperature: 0.5\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Temperature == nil || *cfg.Model.Temperature != 0.5 {
		t.Fatalf("temperature = %#v, want 0.5", cfg.Model.Temperature)
	}
}

func TestLoadKeepsExplicitZeroTemperature(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("OPENAI_API_KEY", "test-secret")
	path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n  temperature: 0\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Temperature == nil || *cfg.Model.Temperature != 0 {
		t.Fatalf("temperature = %#v, want explicit 0", cfg.Model.Temperature)
	}
}

func TestLoadRejectsTemperatureOutOfRange(t *testing.T) {
	for _, value := range []string{"-0.1", "2.1"} {
		t.Run(value, func(t *testing.T) {
			clearPresetAPIKeys(t)
			t.Setenv("OPENAI_API_KEY", "test-secret")
			path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n  temperature: "+value+"\n")
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), "model.temperature must be within [0, 2]") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestDefaultThinkingIsNil(t *testing.T) {
	// The openai preset leaves thinking unset (nil = provider default). Only
	// deepseek defaults thinking to off.
	clearPresetAPIKeys(t)
	t.Setenv("OPENAI_API_KEY", "test-secret")
	cfg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Thinking != nil {
		t.Fatalf("thinking = %#v, want nil", cfg.Model.Thinking)
	}
}

func TestDeepSeekDefaultThinkingIsOff(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	path := writeConfig(t, "model:\n  provider: deepseek\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Thinking == nil || *cfg.Model.Thinking != "off" {
		t.Fatalf("thinking = %#v, want \"off\"", cfg.Model.Thinking)
	}
}

func TestDeepSeekPreservesExplicitThinking(t *testing.T) {
	clearPresetAPIKeys(t)
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	path := writeConfig(t, "model:\n  provider: deepseek\n  thinking: medium\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Thinking == nil || *cfg.Model.Thinking != "medium" {
		t.Fatalf("thinking = %#v, want \"medium\"", cfg.Model.Thinking)
	}
}

func TestLoadAcceptsThinkingValues(t *testing.T) {
	for _, value := range []string{"off", "minimal", "low", "medium", "high"} {
		t.Run(value, func(t *testing.T) {
			clearPresetAPIKeys(t)
			t.Setenv("OPENAI_API_KEY", "test-secret")
			path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n  thinking: "+value+"\n")
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Model.Thinking == nil || *cfg.Model.Thinking != value {
				t.Fatalf("thinking = %#v, want %q", cfg.Model.Thinking, value)
			}
		})
	}
}

func TestLoadRejectsInvalidThinkingValue(t *testing.T) {
	for _, value := range []string{"none", "turbo"} {
		t.Run(value, func(t *testing.T) {
			clearPresetAPIKeys(t)
			t.Setenv("OPENAI_API_KEY", "test-secret")
			path := writeConfig(t, "model:\n  provider: openai\n  name: test-model\n  base_url: http://localhost\n  thinking: "+value+"\n")
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), `model.thinking must be one of "off", "minimal", "low", "medium", "high"`) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLoadRejectsRemovedMaxModelCalls(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
agent:
  max_model_calls: 64
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "max_model_calls") {
		t.Fatalf("error = %v, want unknown max_model_calls", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
  surprise: true
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("error = %v, want unknown field error", err)
	}
}

func TestLoadLiteralAPIKeyIgnoresAPIKeyEnv(t *testing.T) {
	const name = "DORA_TEST_MISSING_API_KEY"
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
  api_key: literal
  api_key_env: DORA_TEST_MISSING_API_KEY
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.APIKey != "literal" {
		t.Fatalf("api key = %q", cfg.Model.APIKey)
	}
}

func TestLoadRejectsMissingEnvironmentAPIKey(t *testing.T) {
	const name = "DORA_TEST_MISSING_API_KEY"
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
  api_key_env: DORA_TEST_MISSING_API_KEY
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), name) {
		t.Fatalf("error = %v, want missing environment error", err)
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearPresetAPIKeys(t *testing.T) {
	t.Helper()
	for _, preset := range modelPresets {
		t.Setenv(preset.apiKeyEnv, "")
	}
}
