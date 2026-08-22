package commandexec

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora/internal/job"
)

func TestExecuteReturnsCommandOutput(t *testing.T) {
	tool := newTestTool(t, Config{})

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"hello"}`))

	output := toolResult.Content
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
	if spec.Name != "test-command" ||
		!strings.Contains(spec.Description, "Execute a test command") ||
		!json.Valid(spec.InputSchema) || !strings.Contains(string(spec.InputSchema), "wait_seconds") {
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
		cfg.JobManager = job.New()
		if _, err := New(cfg); err == nil || !strings.HasPrefix(err.Error(), "test-command:") {
			t.Fatalf("New(%#v) error = %v", cfg, err)
		}
	}
}

func TestNewRequiresJobManager(t *testing.T) {
	if _, err := New(Config{Name: "test-command"}); err == nil || !strings.Contains(err.Error(), "job manager is required") {
		t.Fatalf("New without job manager error = %v", err)
	}
}

func TestExecuteDefaultWaitUsesBackground(t *testing.T) {
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})

	// Default wait_seconds transitions a command that exceeds it to the
	// background instead of terminating it. Override the default to keep the
	// test fast.
	original := defaultWaitSeconds
	defaultWaitSeconds = 1
	t.Cleanup(func() { defaultWaitSeconds = original })

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"long-sleep"}`))
	output := toolResult.Content
	if err != nil {
		t.Fatal(err)
	}
	var bg struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(output), &bg); err != nil {
		t.Fatalf("decode background result: %v (output=%s)", err, output)
	}
	if bg.JobID == "" || bg.Status != "running" {
		t.Fatalf("expected job_id + running, got %#v", bg)
	}

	done, ok := jm.Wait(bg.JobID, 10*time.Second)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != job.StatusDone {
		t.Fatalf("expected done, got %s", done.Status)
	}
}

func TestExecuteZeroWaitAdoptsImmediately(t *testing.T) {
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})

	// wait_seconds = 0 moves the command to the background immediately.
	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"long-sleep","wait_seconds":0}`))
	output := toolResult.Content
	if err != nil {
		t.Fatal(err)
	}
	var bg struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(output), &bg); err != nil {
		t.Fatalf("decode background result: %v (output=%s)", err, output)
	}
	if bg.JobID == "" || bg.Status != "running" {
		t.Fatalf("expected immediate job_id + running, got %#v", bg)
	}
	if !jm.HasActiveJobs() {
		t.Fatalf("expected active job after immediate adoption")
	}

	done, ok := jm.Wait(bg.JobID, 10*time.Second)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != job.StatusDone {
		t.Fatalf("expected done, got %s", done.Status)
	}
}

func TestExecuteNegativeWaitDefaults(t *testing.T) {
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})

	// A negative wait_seconds falls back to the default (60) and uses the
	// background path. Override the default so a long command transitions.
	original := defaultWaitSeconds
	defaultWaitSeconds = 1
	t.Cleanup(func() { defaultWaitSeconds = original })

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"long-sleep","wait_seconds":-5}`))
	output := toolResult.Content
	if err != nil {
		t.Fatal(err)
	}
	var bg struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(output), &bg); err != nil {
		t.Fatalf("decode background result: %v (output=%s)", err, output)
	}
	if bg.JobID == "" || bg.Status != "running" {
		t.Fatalf("expected job_id + running after negative wait, got %#v", bg)
	}
}

func TestExecuteReturnsNonzeroExitToModel(t *testing.T) {
	tool := newTestTool(t, Config{})

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"fail"}`))

	output := toolResult.Content
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if result.ExitCode != 7 || result.Stderr != "problem" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteTransitionsToBackground(t *testing.T) {
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})

	// "long-sleep" sleeps 5s; wait_seconds=1 should transition to background.
	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"long-sleep","wait_seconds":1}`))
	output := toolResult.Content
	if err != nil {
		t.Fatal(err)
	}
	var bg struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(output), &bg); err != nil {
		t.Fatalf("decode background result: %v (output=%s)", err, output)
	}
	if bg.JobID == "" || bg.Status != "running" {
		t.Fatalf("expected job_id + running, got %#v", bg)
	}
	if !jm.HasActiveJobs() {
		t.Fatalf("expected active job after transition")
	}

	// Wait for the job to finish (long-sleep is 2s).
	done, ok := jm.Wait(bg.JobID, 10*time.Second)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != job.StatusDone {
		t.Fatalf("expected done, got %s", done.Status)
	}
	jm.Cleanup()
}

func TestExecuteForegroundWithWaitCompletes(t *testing.T) {
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})

	// "hello" finishes immediately; wait_seconds should return the result.
	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"hello","wait_seconds":5}`))
	output := toolResult.Content
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if result.ExitCode != 0 || result.Stdout != "hello" {
		t.Fatalf("result = %#v", result)
	}
	if jm.HasActiveJobs() {
		t.Fatalf("expected no background job for fast command")
	}
}

func TestExecuteRejectsInvalidInput(t *testing.T) {
	tool := newTestTool(t, Config{})
	for _, raw := range []string{
		`{"command":"hello","extra":1}`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("Execute(%s) succeeded", raw)
		}
	}
}

func TestExecuteStartsWithCanceledContext(t *testing.T) {
	tool := newTestTool(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The background execution path uses an independent context for the
	// process, so an already-canceled request still starts the command.
	_, err := tool.Execute(ctx, json.RawMessage(`{"command":"hello"}`))
	if err != nil {
		t.Fatalf("error = %v", err)
	}
}

func newTestTool(t *testing.T, cfg Config) *Tool {
	t.Helper()
	if cfg.JobManager == nil {
		cfg.JobManager = job.New()
	}
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

	command := os.Args[separator+1]
	switch command {
	case "hello":
		fmt.Print("hello")
	case "fail":
		_, _ = fmt.Fprint(os.Stderr, "problem")
		os.Exit(7)
	case "sleep":
		time.Sleep(time.Second)
	case "long-sleep":
		time.Sleep(2 * time.Second)
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
