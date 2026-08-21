package cli

import (
	"os"
	"path/filepath"
	"testing"
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

func TestSystemPromptReadsAgentsFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Be concise."), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := systemPrompt(); got != "Be concise." {
		t.Fatalf("systemPrompt() = %q, want %q", got, "Be concise.")
	}
}

func TestSystemPromptEmptyWhenAgentsFileMissing(t *testing.T) {
	t.Setenv("DORA_HOME", t.TempDir())

	if got := systemPrompt(); got != "" {
		t.Fatalf("systemPrompt() = %q, want empty", got)
	}
}
