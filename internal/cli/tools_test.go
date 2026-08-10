package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lgxz/dora"
)

func TestAssembleCommandToolsUsesPlatformDefaults(t *testing.T) {
	candidates := []toolCandidate{
		{
			defaultPlatforms: []string{"darwin", "linux"},
			create:           newNamedTool("bash"),
		},
		{
			defaultPlatforms: []string{"windows"},
			create:           newNamedTool("powershell"),
		},
	}
	for _, test := range []struct {
		goos string
		want string
	}{
		{goos: "darwin", want: "bash"},
		{goos: "linux", want: "bash"},
		{goos: "windows", want: "powershell"},
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
			create:           newNamedTool("bash"),
		},
		{
			enabled:          &disabled,
			defaultPlatforms: []string{"windows"},
			create:           newNamedTool("powershell"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(tools); got != "bash" {
		t.Fatalf("tools = %q, want bash", got)
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

func (namedTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

func toolNames(tools []dora.Tool) string {
	names := make([]string, len(tools))
	for index, tool := range tools {
		names[index] = tool.Spec().Name
	}
	return strings.Join(names, ",")
}
