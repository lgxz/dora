package cli

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lgxz/dora/model/router"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
)

func TestBuildObserverColorMode(t *testing.T) {
	tests := []struct {
		name          string
		mode          string
		terminal      bool
		autoColor     bool
		wantANSIColor bool
	}{
		{name: "auto disabled", mode: "auto", autoColor: false, wantANSIColor: false},
		{name: "auto enabled", mode: "auto", terminal: true, autoColor: true, wantANSIColor: true},
		{name: "always overrides disabled auto color", mode: "always", autoColor: false, wantANSIColor: true},
		{name: "never overrides enabled auto color", mode: "never", terminal: true, autoColor: true, wantANSIColor: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr strings.Builder
			observer := buildObserver(IO{
				Stderr:           &stderr,
				TerminalProgress: test.terminal,
				ColorProgress:    test.autoColor,
			}, false, false, test.mode, "")
			observer.Observe(dora.Update{
				Kind: dora.UpdateMessageReceived,
				Message: dora.Message{
					Role:      dora.RoleAssistant,
					Content:   "working",
					ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "skill"}},
				},
			})
			gotANSIColor := strings.Contains(stderr.String(), "\x1b[")
			if gotANSIColor != test.wantANSIColor {
				t.Fatalf("stderr = %q, ANSI color = %v, want %v", stderr.String(), gotANSIColor, test.wantANSIColor)
			}
		})
	}
}

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
		{name: "double slash", in: "a//b", provider: "a", profile: "/b"},
		{name: "empty provider", in: "/profile", wantError: true},
		{name: "empty provider trailing", in: "/", wantError: true},
		{name: "multiple slashes", in: "a/b/c", provider: "a", profile: "b/c"},
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

func TestSystemPromptAppendsEnvironment(t *testing.T) {
	// The local date differs from UTC to catch accidental UTC conversion.
	startedAt := time.Date(2026, 9, 10, 0, 30, 0, 0, time.FixedZone("local", 2*60*60))
	suffix := "\n\n<runtime_environment>\nOS: " + runtime.GOOS +
		"\nArchitecture: " + runtime.GOARCH +
		"\nAgent start date (local): 2026-09-10\n</runtime_environment>"
	for _, tc := range []struct {
		name, configured, base string
	}{
		{"default", "", strings.TrimSpace(defaultSystemPrompt)},
		{"whitespace", " \n ", strings.TrimSpace(defaultSystemPrompt)},
		{"custom", "  You are a pirate.  ", "You are a pirate."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemPrompt(config.Agent{SystemPrompt: tc.configured}, startedAt); got != tc.base+suffix {
				t.Fatalf("system prompt = %q, want %q", got, tc.base+suffix)
			}
		})
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

func TestModelIDFallback(t *testing.T) {
	for _, tc := range []struct {
		name, spec, policy, key, wantModel, thinking string
		wantError                                    bool
		modern                                       bool
	}{
		{name: "raw model", spec: "test/vendor/new-model", key: "key", wantModel: "vendor/new-model"},
		{name: "thinking override", spec: "test/new", key: "key", wantModel: "new", thinking: "high"},
		{name: "modern output budget", spec: "test/known", key: "key", wantModel: "configured-model", modern: true},
		{name: "profile wins", spec: "test/known", key: "key", wantModel: "configured-model"},
		{name: "provider only", spec: "test", key: "key", wantModel: "configured-model"},
		{name: "trailing slash", spec: "test/", key: "key", wantModel: "configured-model"},
		{name: "capability mismatch", spec: "test/vision", key: "key", wantError: true},
		{name: "missing key", spec: "test/new", wantError: true},
		{name: "unknown provider", spec: "missing/new", key: "key", wantError: true},
		{name: "strict policy", policy: "new", key: "key", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget := 42
			cfg := config.Config{
				Providers: []config.Provider{{Name: "test", BaseURL: "https://example.com/v1", API: "chat_completions", APIKey: tc.key,
					Profiles: []config.ProfileSpec{
						{Name: "known", Model: "configured-model", MaxTokens: &budget, UseMaxCompletionTokens: tc.modern, Capabilities: []dora.Capability{dora.CapabilityText}},
						{Name: "vision", Model: "vision-model", Capabilities: []dora.Capability{dora.CapabilityImageInput}},
					}}},
				Policy: config.PolicySettings{Text: config.Policy{Provider: "test", Profile: tc.policy}},
			}
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["model"] != tc.wantModel {
					t.Fatalf("model = %v, want %s", body["model"], tc.wantModel)
				}
				wantBudget := float64(32768)
				if tc.wantModel == "configured-model" {
					wantBudget = 42
				}
				budgetKey, absentKey := "max_tokens", "max_completion_tokens"
				if tc.modern {
					budgetKey, absentKey = absentKey, budgetKey
				}
				if body[budgetKey] != wantBudget {
					t.Fatalf("%s = %v", budgetKey, body[budgetKey])
				}
				if _, ok := body[absentKey]; ok {
					t.Fatalf("unexpected %s", absentKey)
				}
				if tc.thinking != "" && body["reasoning_effort"] != tc.thinking {
					t.Fatalf("thinking = %v", body["reasoning_effort"])
				}
				return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`), nil
			})}
			r, err := buildRuntimeRouter(options{model: tc.spec, thinking: tc.thinking}, cfg, client)
			if tc.wantError {
				if !errors.Is(err, router.ErrNotFound) {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Generate(context.Background(), dora.Request{Messages: []dora.Message{{Role: dora.RoleUser, Content: "hello"}}}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d", calls)
			}
			if len(cfg.Providers[0].Profiles) != 2 {
				t.Fatal("fallback mutated config")
			}
		})
	}
}
