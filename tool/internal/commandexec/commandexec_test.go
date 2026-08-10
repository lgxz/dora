package commandexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExecuteReturnsCommandOutput(t *testing.T) {
	tool := newTestTool(t, Config{})

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if result.ExitCode != 0 || result.Stdout != "hello" || result.Stderr != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestSpecReportsConfiguredDefaultTimeout(t *testing.T) {
	tool := newTestTool(t, Config{Timeout: 45 * time.Second})
	spec := tool.Spec()
	if spec.Name != "test-command" || spec.Description != "Execute a test command" ||
		!json.Valid(spec.InputSchema) || !strings.Contains(string(spec.InputSchema), "default of 45s") {
		t.Fatalf("spec = %#v", spec)
	}
}

func TestNewRejectsInvalidLimits(t *testing.T) {
	for _, cfg := range []Config{
		{Timeout: -time.Second},
		{Timeout: time.Hour + time.Second},
		{MaxOutputBytes: -1},
	} {
		cfg.Name = "test-command"
		if _, err := New(cfg); err == nil || !strings.HasPrefix(err.Error(), "test-command:") {
			t.Fatalf("New(%#v) error = %v", cfg, err)
		}
	}
}

func TestExecuteReturnsNonzeroExitToModel(t *testing.T) {
	tool := newTestTool(t, Config{})

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"fail"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if result.ExitCode != 7 || result.Stderr != "problem" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteTimesOut(t *testing.T) {
	tool := newTestTool(t, Config{Timeout: 20 * time.Millisecond})

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"sleep"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if !result.TimedOut || result.ExitCode != -1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteUsesPerCommandTimeout(t *testing.T) {
	tool := newTestTool(t, Config{Timeout: time.Nanosecond})

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"short-sleep","timeout_seconds":5}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if result.TimedOut || result.Stdout != "done" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteRejectsInvalidInput(t *testing.T) {
	tool := newTestTool(t, Config{})
	for _, raw := range []string{
		`{"command":"hello","extra":1}`,
		`{"command":"hello","timeout_seconds":0}`,
		`{"command":"hello","timeout_seconds":3601}`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("Execute(%s) succeeded", raw)
		}
	}
}

func TestExecuteTruncatesOutput(t *testing.T) {
	tool := newTestTool(t, Config{MaxOutputBytes: 4})

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"output"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if !result.Truncated || result.Stdout != "1234" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteHonorsCancellation(t *testing.T) {
	tool := newTestTool(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := tool.Execute(ctx, json.RawMessage(`{"command":"hello"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func newTestTool(t *testing.T, cfg Config) *Tool {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Name = "test-command"
	cfg.Description = "Execute a test command"
	cfg.Binary = binary
	cfg.CommandArgs = func(command string) []string {
		return []string{"-test.run=TestCommandHelper", "--", command}
	}
	tool, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func decodeResult(t *testing.T, output string) commandResult {
	t.Helper()
	var result commandResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCommandHelper(t *testing.T) {
	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator == -1 || separator+1 >= len(os.Args) {
		return
	}

	switch os.Args[separator+1] {
	case "hello":
		fmt.Print("hello")
	case "fail":
		_, _ = fmt.Fprint(os.Stderr, "problem")
		os.Exit(7)
	case "sleep":
		time.Sleep(time.Second)
	case "short-sleep":
		time.Sleep(100 * time.Millisecond)
		fmt.Print("done")
	case "output":
		fmt.Print("123456")
	default:
		os.Exit(2)
	}
	os.Exit(0)
}
