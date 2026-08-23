package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgxz/dora/internal/config"
)

func TestParseModelSpec(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		provider  string
		profile   string
		wantError bool
	}{
		{name: "provider and profile", in: "trust/deepseek-v4-flash", provider: "trust", profile: "deepseek-v4-flash"},
		{name: "trailing slash", in: "trust/", provider: "trust", profile: ""},
		{name: "provider only", in: "trust", provider: "trust", profile: ""},
		{name: "empty", in: "", wantError: true},
		{name: "double slash", in: "a//b", wantError: true},
		{name: "empty provider", in: "/profile", wantError: true},
		{name: "empty provider trailing", in: "/", wantError: true},
		{name: "multiple slashes", in: "a/b/c", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, profile, err := parseModelSpec(tc.in)
			if tc.wantError {
				if err == nil {
					t.Fatalf("parseModelSpec(%q) = (%q, %q), want error", tc.in, provider, profile)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseModelSpec(%q) returned error: %v", tc.in, err)
			}
			if provider != tc.provider || profile != tc.profile {
				t.Fatalf("parseModelSpec(%q) = (%q, %q), want (%q, %q)", tc.in, provider, profile, tc.provider, tc.profile)
			}
		})
	}
}

func TestSystemPromptAppendsAgentsFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Be concise."), 0o600); err != nil {
		t.Fatal(err)
	}

	want := defaultSystemPrompt + "\n\n" + "Be concise."
	if got := systemPrompt(config.Agent{}); got != want {
		t.Fatalf("systemPrompt(Agent{}) = %q, want default prompt followed by AGENTS.md content", got)
	}
}

func TestSystemPromptDefaultsWhenAgentsFileMissing(t *testing.T) {
	t.Setenv("DORA_HOME", t.TempDir())

	if got := systemPrompt(config.Agent{}); got != defaultSystemPrompt {
		t.Fatalf("systemPrompt(Agent{}) = %q, want the built-in default prompt", got)
	}
}

func TestSystemPromptConfigOverridesDefault(t *testing.T) {
	t.Setenv("DORA_HOME", t.TempDir())

	agent := config.Agent{SystemPrompt: "You are a pirate."}
	if got := systemPrompt(agent); got != "You are a pirate." {
		t.Fatalf("systemPrompt(%+v) = %q, want the configured prompt verbatim", agent, got)
	}
}

func TestSystemPromptConfigOverridesDefaultButAppendsAgentsFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Be concise."), 0o600); err != nil {
		t.Fatal(err)
	}

	agent := config.Agent{SystemPrompt: "  You are a pirate.  "}
	want := "You are a pirate.\n\nBe concise."
	if got := systemPrompt(agent); got != want {
		t.Fatalf("systemPrompt(%+v) = %q, want %q", agent, got, want)
	}
}

func TestDefaultSystemPromptContent(t *testing.T) {
	// The prompt wording is intentionally not anchored here: this only guards
	// against the embedded file being emptied or renamed, which would
	// silently disable the system prompt.
	if strings.TrimSpace(defaultSystemPrompt) == "" {
		t.Fatal("defaultSystemPrompt is empty")
	}
}
