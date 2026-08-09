package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadResolvesEnvironmentAPIKey(t *testing.T) {
	t.Setenv("DORA_TEST_API_KEY", "secret")
	path := writeConfig(t, `
model:
  provider: openai-compatible
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

func TestLoadAcceptsOpenAIResponsesProvider(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai-responses
  name: gpt-5
  base_url: https://api.openai.com/v1
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "openai-responses" {
		t.Fatalf("provider = %q", cfg.Model.Provider)
	}
}

func TestLoadAcceptsSkillDirectories(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai-compatible
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
  provider: openai-compatible
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

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, `
model:
  provider: openai-compatible
  name: test-model
  base_url: http://localhost
  surprise: true
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("error = %v, want unknown field error", err)
	}
}

func TestLoadRejectsTwoAPIKeySources(t *testing.T) {
	t.Setenv("DORA_TEST_API_KEY", "secret")
	path := writeConfig(t, `
model:
  provider: openai-compatible
  name: test-model
  base_url: http://localhost
  api_key: literal
  api_key_env: DORA_TEST_API_KEY
`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v, want mutually exclusive error", err)
	}
}

func TestLoadRejectsMissingEnvironmentAPIKey(t *testing.T) {
	const name = "DORA_TEST_MISSING_API_KEY"
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, `
model:
  provider: openai-compatible
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
