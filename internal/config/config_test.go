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
