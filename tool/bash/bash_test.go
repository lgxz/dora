package bash

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/lgxz/dora/internal/job"
)

func TestExecuteReturnsCommandOutput(t *testing.T) {
	tool, err := New(Config{JobManager: job.New()})
	if err != nil {
		t.Fatal(err)
	}

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"printf hello"}`))

	output := toolResult.Content
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		ExitCode int    `json:"exit_code"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || result.Stdout != "hello" || result.Stderr != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewReportsUnavailableExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := New(Config{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestSpecIdentifiesBash(t *testing.T) {
	tool, err := New(Config{JobManager: job.New()})
	if err != nil {
		t.Fatal(err)
	}
	spec := tool.Spec()
	if spec.Name != "bash" ||
		!strings.Contains(spec.Description, "Execute Bash command on "+runtime.GOOS+"/"+runtime.GOARCH) ||
		!json.Valid(spec.InputSchema) || !strings.Contains(string(spec.InputSchema), "wait_seconds") {
		t.Fatalf("spec = %#v", spec)
	}
}

func TestCommandArgsUseLoginShell(t *testing.T) {
	want := []string{"-lc", "printf hello"}
	if got := commandArgs("printf hello"); !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestExecuteOnNilTool(t *testing.T) {
	var tool *Tool
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"true"}`))
	if err == nil || err.Error() != "bash: tool is not initialized" {
		t.Fatalf("error = %v", err)
	}
}
