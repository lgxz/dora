package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lgxz/dora/internal/config"
)

func TestRunSetupCreatesSecureUsableConfig(t *testing.T) {
	clearBuiltinProviderKeys(t)
	configPath := filepath.Join(t.TempDir(), "nested", "config.yaml")
	var stdout strings.Builder
	err := Run(context.Background(), []string{"--setup", "--config", configPath}, IO{
		Stdin:           strings.NewReader("1\n\n"),
		Stdout:          &stdout,
		Stderr:          &strings.Builder{},
		StdinIsTerminal: true,
		ReadSecret: func() (string, error) {
			return "deepseek-secret", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "deepseek-secret") {
		t.Fatal("setup output exposed the API key")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policy.Text.Provider != "deepseek" || cfg.Policy.Text.Profile != "" {
		t.Fatalf("text policy = %#v", cfg.Policy.Text)
	}
	if got := providerAPIKey(t, cfg, "deepseek"); got != "deepseek-secret" {
		t.Fatalf("API key = %q", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("config mode = %o, want 600", got)
		}
	}
}

func TestRunSetupPreservesExistingConfigAndCanSelectProfile(t *testing.T) {
	clearBuiltinProviderKeys(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	existing := `# keep this configuration
env:
  DEEPSEEK_API_KEY: old-key
policy:
  text:
    provider: deepseek
agent:
  max_rounds: 42
`
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), []string{"--acp", "--setup", "--config", configPath}, IO{
		Stdin:  strings.NewReader("2\n3\ntrust-secret\n"),
		Stdout: &strings.Builder{},
		Stderr: &strings.Builder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# keep this configuration") {
		t.Fatal("setup did not preserve the existing comment")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.MaxRounds != 42 {
		t.Fatalf("max rounds = %d", cfg.Agent.MaxRounds)
	}
	if cfg.Policy.Text.Provider != "trust" || cfg.Policy.Text.Profile != "qwen3.7-plus" {
		t.Fatalf("text policy = %#v", cfg.Policy.Text)
	}
	if got := providerAPIKey(t, cfg, "deepseek"); got != "old-key" {
		t.Fatalf("preserved API key = %q", got)
	}
	if got := providerAPIKey(t, cfg, "trust"); got != "trust-secret" {
		t.Fatalf("new API key = %q", got)
	}
}

func TestRunSetupRejectsPrompt(t *testing.T) {
	err := Run(context.Background(), []string{"--setup", "prompt"}, IO{
		Stdin:  strings.NewReader(""),
		Stdout: &strings.Builder{},
		Stderr: &strings.Builder{},
	})
	if err == nil || !strings.Contains(err.Error(), "does not accept a prompt") {
		t.Fatalf("error = %v", err)
	}
}

func clearBuiltinProviderKeys(t *testing.T) {
	t.Helper()
	for _, name := range []string{"DEEPSEEK_API_KEY", "TRUST_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(name, "")
	}
}

func providerAPIKey(t *testing.T, cfg config.Config, name string) string {
	t.Helper()
	for _, provider := range cfg.Providers {
		if provider.Name == name {
			return provider.APIKey
		}
	}
	t.Fatalf("provider %q not found", name)
	return ""
}
