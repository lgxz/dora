package cli

import (
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

func TestSystemPromptUsesDefault(t *testing.T) {
	if got := systemPrompt(config.Agent{}); got != defaultSystemPrompt {
		t.Fatalf("systemPrompt(Agent{}) = %q, want the built-in default prompt", got)
	}
}

func TestSystemPromptConfigOverridesDefault(t *testing.T) {
	agent := config.Agent{SystemPrompt: "  You are a pirate.  "}
	if got := systemPrompt(agent); got != "You are a pirate." {
		t.Fatalf("systemPrompt(%+v) = %q, want the configured prompt verbatim", agent, got)
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
