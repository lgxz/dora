package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	tools, err := buildTools(config.Config{}, toolDependencies{
		model:    r,
		jobs:     job.New(),
		noSkills: true,
		taskRunner: func(context.Context, string) (string, error) {
			return "done", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toolNames(tools), "view_image") {
		t.Fatalf("tools = %q, want view_image present", toolNames(tools))
	}
}

func TestLimitTasksBoundsConcurrency(t *testing.T) {
	var running, peak atomic.Int32
	release := make(chan struct{})
	run := limitTasks(2, func(context.Context, string) (string, error) {
		now := running.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		<-release
		running.Add(-1)
		return "ok", nil
	})
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if result, err := run(context.Background(), "x"); err != nil || result != "ok" {
				t.Errorf("run = %q, %v", result, err)
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for running.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // give excess runs the chance to start wrongly
	if got := running.Load(); got != 2 {
		t.Fatalf("running = %d, want 2", got)
	}
	close(release)
	wg.Wait()
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak concurrency = %d, want 2", got)
	}
}

func TestLimitTasksWaitEndsWithContext(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	started := make(chan struct{})
	run := limitTasks(1, func(ctx context.Context, instruction string) (string, error) {
		if instruction == "first" {
			close(started)
			<-block
			return "done", nil
		}
		t.Error("waiting run executed")
		return "", nil
	})
	go run(context.Background(), "first")
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := run(ctx, "second"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestLimitTasksDefaultsWhenUnset(t *testing.T) {
	var running, peak atomic.Int32
	release := make(chan struct{})
	run := limitTasks(0, func(context.Context, string) (string, error) {
		now := running.Add(1)
		if now > peak.Load() {
			peak.Store(now)
		}
		<-release
		running.Add(-1)
		return "", nil
	})
	var wg sync.WaitGroup
	for range config.DefaultTaskConcurrency + 3 {
		wg.Add(1)
		go func() { defer wg.Done(); run(context.Background(), "x") }()
	}
	time.Sleep(50 * time.Millisecond)
	if got := running.Load(); got != config.DefaultTaskConcurrency {
		t.Fatalf("running = %d, want %d", got, config.DefaultTaskConcurrency)
	}
	close(release)
	wg.Wait()
}
