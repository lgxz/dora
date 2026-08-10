package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultUsesDeepSeek(t *testing.T) {
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
	if !cfg.Tools.Bash.Enabled || !cfg.Tools.PowerShell.Enabled {
		t.Fatalf("tools = %#v", cfg.Tools)
	}
}

func TestLoadUsesDeepSeekWhenProviderIsOmitted(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	path := writeConfig(t, "agent:\n  max_model_calls: 32\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "deepseek" || cfg.Model.Name != "deepseek-v4-flash" || cfg.Agent.MaxModelCalls != 32 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestLoadEnablesBashByDefault(t *testing.T) {
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
	if !cfg.Tools.Bash.Enabled {
		t.Fatal("bash is disabled, want default enabled")
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
	if cfg.Tools.Bash.Enabled {
		t.Fatal("bash is enabled after explicit disable")
	}
}

func TestLoadEnablesPowerShellByDefault(t *testing.T) {
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
	if !cfg.Tools.PowerShell.Enabled {
		t.Fatal("powershell is disabled, want default enabled")
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
	if cfg.Tools.PowerShell.Enabled {
		t.Fatal("powershell is enabled after explicit disable")
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
	path := writeConfig(t, "model:\n  provider: openai-compatible\n")

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `must be "openai" or "deepseek"`) {
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
  max_model_calls: 96
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.MaxModelCalls != 96 {
		t.Fatalf("max model calls = %d", cfg.Agent.MaxModelCalls)
	}
}

func TestLoadRejectsNegativeAgentModelCallLimit(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai
  name: test-model
  base_url: http://localhost
agent:
  max_model_calls: -1
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "cannot be negative") {
		t.Fatalf("error = %v", err)
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
	t.Setenv("OPENAI_API_KEY", "test-secret")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
