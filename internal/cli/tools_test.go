package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/model/registry"
	"github.com/lgxz/dora/model/router"
)

func TestAssembleCommandToolsUsesPlatformDefaults(t *testing.T) {
	candidates := []toolCandidate{
		{
			defaultPlatforms: []string{"darwin", "linux"},
			create:           newNamedTool("Bash"),
		},
		{
			defaultPlatforms: []string{"windows"},
			create:           newNamedTool("PowerShell"),
		},
	}
	for _, test := range []struct {
		goos string
		want string
	}{
		{goos: "darwin", want: "Bash"},
		{goos: "linux", want: "Bash"},
		{goos: "windows", want: "PowerShell"},
		{goos: "plan9", want: ""},
	} {
		t.Run(test.goos, func(t *testing.T) {
			tools, err := assembleCommandTools(test.goos, candidates)
			if err != nil {
				t.Fatal(err)
			}
			if got := toolNames(tools); got != test.want {
				t.Fatalf("tools = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAssembleCommandToolsExplicitConfigOverridesPlatform(t *testing.T) {
	enabled, disabled := true, false
	tools, err := assembleCommandTools("windows", []toolCandidate{
		{
			enabled:          &enabled,
			defaultPlatforms: []string{"darwin", "linux"},
			create:           newNamedTool("Bash"),
		},
		{
			enabled:          &disabled,
			defaultPlatforms: []string{"windows"},
			create:           newNamedTool("PowerShell"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(tools); got != "Bash" {
		t.Fatalf("tools = %q, want Bash", got)
	}
}

func TestAssembleCommandToolsHandlesUnavailableCandidates(t *testing.T) {
	unavailable := errors.New("unavailable")
	candidate := toolCandidate{
		defaultPlatforms: []string{"linux"},
		create: func() (dora.Tool, error) {
			return nil, fmt.Errorf("find tool: %w", unavailable)
		},
		unavailable: unavailable,
	}
	tools, err := assembleCommandTools("linux", []toolCandidate{candidate})
	if err != nil || len(tools) != 0 {
		t.Fatalf("automatic unavailable result = (%#v, %v)", tools, err)
	}

	enabled := true
	candidate.enabled = &enabled
	_, err = assembleCommandTools("linux", []toolCandidate{candidate})
	if !errors.Is(err, unavailable) {
		t.Fatalf("explicit unavailable error = %v", err)
	}
}

type namedTool string

func newNamedTool(name string) func() (dora.Tool, error) {
	return func() (dora.Tool, error) { return namedTool(name), nil }
}

func (tool namedTool) Spec() dora.ToolSpec {
	return dora.ToolSpec{Name: string(tool), InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (namedTool) Execute(context.Context, json.RawMessage) (dora.ToolResult, error) {
	return dora.ToolResult{}, nil
}

func toolNames(tools []dora.Tool) string {
	names := make([]string, len(tools))
	for index, tool := range tools {
		names[index] = tool.Spec().Name
	}
	return strings.Join(names, ",")
}

func TestBuildToolsAlwaysRegistersViewImage(t *testing.T) {
	cat, err := registry.NewCatalog(registry.Config{Providers: []registry.ProviderConfig{{
		Name: "p", BaseURL: "https://example.test/v1", API: "chat_completions", APIKey: "test-key",
		Profiles: []registry.Profile{{
			Name: "text", Capabilities: []dora.Capability{dora.CapabilityText},
		}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := router.New(cat, dora.Constraints{Needs: []dora.Capability{dora.CapabilityText}}, dora.Constraints{Needs: []dora.Capability{dora.CapabilityImageInput}})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := buildTools(config.Config{}, r, job.New(), nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toolNames(tools), "ViewImage") {
		t.Fatalf("tools = %q, want ViewImage present", toolNames(tools))
	}
}
