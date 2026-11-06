package commandexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/job"
	jobtool "github.com/lgxz/dora/tool/job"
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

func TestSpecReportsWaitSeconds(t *testing.T) {
	tool := newTestTool(t, Config{})
	spec := tool.Spec()
	if spec.Name != "test-command" ||
		!strings.Contains(spec.Description, "Execute a test command") ||
		!json.Valid(spec.InputSchema) || !strings.Contains(string(spec.InputSchema), "wait_seconds") {
		t.Fatalf("spec = %#v", spec)
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
	bg := decodeResult(t, output)
	if bg.JobID != "test-command_0" || !bg.Background {
		t.Fatalf("expected job_id + running, got %#v", bg)
	}

	done, ok := jm.Poll(bg.JobID, 10*time.Second)
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
	bg := decodeResult(t, output)
	if bg.JobID != "test-command_0" || !bg.Background {
		t.Fatalf("expected immediate job_id + running, got %#v", bg)
	}
	if !jm.HasActiveJobs() {
		t.Fatalf("expected active job after immediate adoption")
	}

	done, ok := jm.Poll(bg.JobID, 10*time.Second)
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
	bg := decodeResult(t, output)
	if bg.JobID != "test-command_0" || !bg.Background {
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
	bg := decodeResult(t, output)
	if bg.JobID != "test-command_0" || !bg.Background {
		t.Fatalf("expected job_id + running, got %#v", bg)
	}
	if !jm.HasActiveJobs() {
		t.Fatalf("expected active job after transition")
	}

	// Wait for the job to finish (long-sleep is 2s).
	done, ok := jm.Poll(bg.JobID, 10*time.Second)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != job.StatusDone {
		t.Fatalf("expected done, got %s", done.Status)
	}
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

func TestExecuteDetachedChildReturnsSuccess(t *testing.T) {
	tool := newTestTool(t, Config{})

	// "background-child" exits 0 after spawning a child that inherits the
	// output pipes and outlives the WaitDelay, so cmd.Wait() returns
	// exec.ErrWaitDelay. The command itself succeeded and must be reported
	// as a normal exit 0 result, not a tool error.
	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"background-child","wait_seconds":5}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, toolResult.Content)
	if result.ExitCode != 0 || result.Stdout != "parent-done" {
		t.Fatalf("result = %#v", result)
	}
}

func TestKillTerminatesCommandDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process-group behavior")
	}
	root := t.TempDir()
	startedPath := filepath.Join(root, "child-started")
	markerPath := filepath.Join(root, "child-survived")
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(fmt.Sprintf(
		`{"command":%q,"wait_seconds":0}`, "spawn-child:"+startedPath+":"+markerPath,
	)))
	if err != nil {
		t.Fatal(err)
	}
	background := decodeResult(t, toolResult.Content)
	if !background.Background {
		t.Fatalf("expected a background job, got %q", toolResult.Content)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := jm.Kill(background.JobID); err != nil {
		t.Fatal(err)
	}
	done, ok := jm.Poll(background.JobID, 2*time.Second)
	if !ok || done.Status != job.StatusKilled {
		t.Fatalf("killed command = %#v, ok = %v", done, ok)
	}
	time.Sleep(750 * time.Millisecond)
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("descendant survived command kill")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestExecuteCapsLargeOutput(t *testing.T) {
	tool := newTestTool(t, Config{})

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"big-output"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, toolResult.Content)
	if !strings.HasPrefix(result.Stdout, "HEAD-MARKER\n") {
		t.Fatalf("head not preserved: %q", result.Stdout[:min(20, len(result.Stdout))])
	}
	if !strings.HasSuffix(result.Stdout, "TAIL-MARKER") {
		t.Fatalf("tail not preserved: %q", result.Stdout[max(0, len(result.Stdout)-20):])
	}
	if !strings.Contains(result.Stdout, "bytes total") {
		t.Fatal("stdout truncation marker missing")
	}
	if len(result.Stdout) > job.MaxResultBytes+256 {
		t.Fatalf("stdout not capped: %d bytes", len(result.Stdout))
	}
	if !strings.Contains(result.Stderr, "bytes total") || len(result.Stderr) > job.MaxResultBytes+256 {
		t.Fatalf("stderr not capped: %d bytes", len(result.Stderr))
	}
}

func TestExecuteBoundsHugeOutput(t *testing.T) {
	tool := newTestTool(t, Config{})

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"huge-output"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, toolResult.Content)
	total := len("HEAD-MARKER\n") + (64*1024-1)*1024 + len("TAIL-MARKER")
	if !strings.HasPrefix(result.Stdout, "HEAD-MARKER\n") || !strings.HasSuffix(result.Stdout, "TAIL-MARKER") {
		t.Fatalf("head/tail not preserved: %q ... %q", result.Stdout[:20], result.Stdout[len(result.Stdout)-20:])
	}
	if want := fmt.Sprintf("%d bytes total", total); !strings.Contains(result.Stdout, want) {
		t.Fatalf("marker should report the true size (%s)", want)
	}
	if len(result.Stdout) > job.MaxResultBytes {
		t.Fatalf("stdout = %d bytes, want <= %d", len(result.Stdout), job.MaxResultBytes)
	}
}

func TestExecuteCapsOutputOnBackgroundAdoption(t *testing.T) {
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"big-output-slow","wait_seconds":1}`))
	if err != nil {
		t.Fatal(err)
	}
	bg := decodeResult(t, toolResult.Content)
	if bg.JobID == "" || !bg.Background {
		t.Fatalf("expected job_id + running, got %#v", bg)
	}
	if !strings.HasPrefix(bg.Stdout, "HEAD-MARKER\n") || !strings.Contains(bg.Stdout, "bytes total") {
		t.Fatalf("adoption stdout not capped with marker: %d bytes", len(bg.Stdout))
	}
	if len(bg.Stdout) > job.MaxResultBytes+256 {
		t.Fatalf("adoption stdout not capped: %d bytes", len(bg.Stdout))
	}
	if _, err := jm.Kill(bg.JobID); err != nil {
		t.Fatalf("kill: %v", err)
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

func TestExecuteDoesNotStartWithCanceledContext(t *testing.T) {
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// A request that is already canceled must not start a process at all: the
	// helper would create the marker half a second after starting.
	_, err := tool.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"command":%q,"wait_seconds":0}`, "write-after:"+marker)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	time.Sleep(900 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the command ran despite the canceled context")
	}
	if jm.HasActiveJobs() {
		t.Fatal("a canceled request left a background job")
	}
}

// waitForFile polls until path exists, failing the test after a generous limit.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

func TestExecuteCancelDuringForegroundWaitKillsProcessTree(t *testing.T) {
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	survived := filepath.Join(dir, "survived")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The parent starts a child in the same process group, then sleeps for 10s.
	// The child would write "survived" half a second after starting.
	command := fmt.Sprintf(`{"command":%q,"wait_seconds":600}`, "spawn-child:"+started+":"+survived)
	type outcome struct {
		result dora.ToolResult
		err    error
		took   time.Duration
	}
	finished := make(chan outcome, 1)
	begin := time.Now()
	go func() {
		result, err := tool.Execute(ctx, json.RawMessage(command))
		finished <- outcome{result, err, time.Since(begin)}
	}()
	waitForFile(t, started)
	canceledAt := time.Now()
	cancel()

	select {
	case got := <-finished:
		if got.err != nil {
			t.Fatalf("Execute error = %v, want a canceled result", got.err)
		}
		if elapsed := time.Since(canceledAt); elapsed > 5*time.Second {
			t.Fatalf("Execute took %s to notice cancellation, want it prompt", elapsed)
		}
		result := decodeResult(t, got.result.Content)
		if !result.Canceled {
			t.Fatalf("result = %#v, want canceled", result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Execute did not return after cancellation (wait_seconds was 600)")
	}
	if jm.HasActiveJobs() {
		t.Fatal("canceled foreground command was adopted as a background job")
	}
	// Give a surviving child time to write its marker; the whole tree is dead.
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(survived); err == nil {
		t.Fatal("a descendant of the canceled command survived")
	}
}

func TestExecuteCancelReturnsOutputCapturedSoFar(t *testing.T) {
	tool := newTestTool(t, Config{})
	started := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	finished := make(chan dora.ToolResult, 1)
	go func() {
		result, err := tool.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"command":%q,"wait_seconds":600}`, "print-hang:"+started)))
		if err != nil {
			t.Errorf("Execute error = %v", err)
		}
		finished <- result
	}()
	waitForFile(t, started)
	cancel()

	select {
	case got := <-finished:
		result := decodeResult(t, got.Content)
		if !result.Canceled || result.Stdout != "partial-out" || result.Stderr != "partial-err" {
			t.Fatalf("result = %#v, want the output printed before cancellation", result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Execute did not return after cancellation")
	}
}

type scriptedModel func(context.Context, dora.Request) (dora.Response, error)

func (f scriptedModel) Generate(ctx context.Context, request dora.Request) (dora.Response, error) {
	return f(ctx, request)
}

// The Agent waits for every running tool before it can stop, so a command that
// ignores cancellation would keep the whole run (and the unsaved Turn) hostage
// for up to wait_seconds. This drives the real Agent loop to check that.
func TestAgentRunStopsPromptlyWhenCanceledDuringForegroundCommand(t *testing.T) {
	tool := newTestTool(t, Config{})
	started := filepath.Join(t.TempDir(), "started")
	input := fmt.Sprintf(`{"command":%q,"wait_seconds":600}`, "print-hang:"+started)
	var calls int
	model := scriptedModel(func(context.Context, dora.Request) (dora.Response, error) {
		calls++
		return dora.Response{FinishReason: dora.FinishToolCalls, ToolCalls: []dora.ToolCall{
			{ID: "call-1", Name: "test-command", Input: json.RawMessage(input)},
		}}, nil
	})
	agent, err := dora.New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	turn := dora.NewTurn("run something that hangs")
	finished := make(chan error, 1)
	go func() { finished <- agent.Run(ctx, turn) }()
	waitForFile(t, started)
	cancel()

	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancellation while a foreground command was running")
	}
	if calls != 1 {
		t.Fatalf("model calls = %d, want 1 (no request after cancellation)", calls)
	}
	rounds := turn.Rounds()
	if len(rounds) != 1 || len(rounds[0].Tools) != 1 {
		t.Fatalf("rounds = %#v, want the interrupted round to be kept", rounds)
	}
	result := decodeResult(t, rounds[0].Tools[0].Content)
	if !result.Canceled || result.Stdout != "partial-out" {
		t.Fatalf("saved tool result = %#v, want canceled with the partial output", result)
	}
}

func TestExecuteCancelAfterAdoptionLeavesBackgroundJobRunning(t *testing.T) {
	jm := job.New()
	tool := newTestTool(t, Config{JobManager: jm})
	ctx, cancel := context.WithCancel(context.Background())

	// wait_seconds=0 adopts immediately. Once adopted the command has its own
	// lifetime: canceling the run that started it must not kill it.
	toolResult, err := tool.Execute(ctx, json.RawMessage(`{"command":"long-sleep","wait_seconds":0}`))
	if err != nil {
		t.Fatal(err)
	}
	bg := decodeResult(t, toolResult.Content)
	if bg.JobID == "" || !bg.Background {
		t.Fatalf("background result = %s", toolResult.Content)
	}
	cancel()
	done, ok := jm.Poll(bg.JobID, 15*time.Second)
	if !ok || done.Status != job.StatusDone {
		t.Fatalf("job after run cancellation = %#v, ok = %v; want it to finish normally", done, ok)
	}
}

func TestExecuteDeadlineDuringForegroundWaitCancelsCommand(t *testing.T) {
	tool := newTestTool(t, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	begin := time.Now()
	toolResult, err := tool.Execute(ctx, json.RawMessage(`{"command":"sleep","wait_seconds":600}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(begin); elapsed > 10*time.Second {
		t.Fatalf("Execute took %s despite a 300ms deadline", elapsed)
	}
	if result := decodeResult(t, toolResult.Content); !result.Canceled {
		t.Fatalf("result = %#v, want canceled", result)
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

func decodeResult(t *testing.T, output string) job.Result {
	t.Helper()
	return job.ParseResult(output)
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
	if strings.HasPrefix(command, "spawn-child:") {
		paths := strings.SplitN(strings.TrimPrefix(command, "spawn-child:"), ":", 2)
		if len(paths) != 2 {
			os.Exit(2)
		}
		child := exec.Command(os.Args[0], "-test.run=TestCommandHelper", "--", "write-after:"+paths[1])
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(paths[0], []byte("started"), 0o600); err != nil {
			os.Exit(4)
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	if strings.HasPrefix(command, "print-hang:") {
		fmt.Print("partial-out")
		fmt.Fprint(os.Stderr, "partial-err")
		if err := os.WriteFile(strings.TrimPrefix(command, "print-hang:"), []byte("started"), 0o600); err != nil {
			os.Exit(4)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	if strings.HasPrefix(command, "write-after:") {
		time.Sleep(500 * time.Millisecond)
		if err := os.WriteFile(strings.TrimPrefix(command, "write-after:"), []byte("survived"), 0o600); err != nil {
			os.Exit(5)
		}
		os.Exit(0)
	}
	switch command {
	case "escaped-output", "escaped-output-slow":
		fmt.Print(escapedOutput)
		fmt.Fprint(os.Stderr, escapedOutput)
		if command == "escaped-output-slow" {
			time.Sleep(2 * time.Second)
			fmt.Print(escapedOutput)
			fmt.Fprint(os.Stderr, escapedOutput)
		}
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
	case "background-child":
		// Spawn a child that inherits this process's stdout/stderr (the
		// tool's pipes) and outlives the parent, mimicking `server &`.
		child := exec.Command(os.Args[0], "-test.run=TestCommandHelper", "--", "long-sleep")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		fmt.Print("parent-done")
	case "big-output":
		// ~100KB of stdout plus a matching stderr, with identifiable ends.
		fmt.Print("HEAD-MARKER\n")
		for i := 0; i < 2000; i++ {
			fmt.Printf("line-%05d padding padding padding\n", i)
		}
		fmt.Print("TAIL-MARKER")
		fmt.Fprint(os.Stderr, strings.Repeat("e", 60_000))
	case "huge-output":
		// 64 MiB of stdout in 1 KiB lines, with identifiable ends.
		w := bufio.NewWriter(os.Stdout)
		fmt.Fprint(w, "HEAD-MARKER\n")
		line := strings.Repeat("h", 1023) + "\n"
		for i := 0; i < 64*1024-1; i++ {
			w.WriteString(line)
		}
		fmt.Fprint(w, "TAIL-MARKER")
		w.Flush()
	case "big-output-slow":
		// Emits oversized output quickly, then keeps running so the tool
		// adopts it as a background job with buffered output.
		fmt.Print("HEAD-MARKER\n")
		fmt.Print(strings.Repeat("o", 60_000))
		time.Sleep(2 * time.Second)
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

// Include Go-only escapes, JSON escapes, Unicode, and HTML-sensitive characters.
const escapedOutput = "\x1b[31mred\x1b[0m\x00\a\v\n\t\r\b\f\"\\中文<&>"

func TestCommandOutputIsVerbatim(t *testing.T) {
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprint("background=", background), func(t *testing.T) {
			jm := job.New()
			tool := newTestTool(t, Config{JobManager: jm})
			command := "escaped-output"
			wait := 5
			if background {
				command = "escaped-output-slow"
				wait = 1
			}
			raw, err := json.Marshal(map[string]any{"command": command, "wait_seconds": wait})
			if err != nil {
				t.Fatal(err)
			}
			result, err := tool.Execute(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			// The model must see the bytes as printed: no JSON escaping.
			body := escapedOutput + "\n[stderr]\n" + escapedOutput
			if !background {
				if result.Content != body {
					t.Fatalf("result = %q, want %q", result.Content, body)
				}
				return
			}
			t.Cleanup(func() { _, _ = jm.Kill("test-command_0") })
			if !strings.HasSuffix(result.Content, "\n"+body) || !strings.HasPrefix(result.Content, "[background job test-command_0:") {
				t.Fatalf("background result = %q", result.Content)
			}
			raw, err = json.Marshal(map[string]any{"action": "poll", "job_id": "test-command_0", "wait_seconds": 5})
			if err != nil {
				t.Fatal(err)
			}
			result, err = jobtool.New(jm).Execute(context.Background(), raw)
			if err != nil {
				t.Fatal(err)
			}
			if want := "[job test-command_0: done, exit code 0]\n" + body; result.Content != want {
				t.Fatalf("poll result = %q, want %q", result.Content, want)
			}
		})
	}
}
