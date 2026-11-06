package bash

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	if output != "hello" {
		t.Fatalf("output = %q, want plain stdout", output)
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
		!strings.HasPrefix(spec.Description, "Execute Bash command.") ||
		!json.Valid(spec.InputSchema) || !strings.Contains(string(spec.InputSchema), "wait_seconds") {
		t.Fatalf("spec = %#v", spec)
	}
}

func TestCommandArgsUseNonLoginShell(t *testing.T) {
	want := []string{"-c", "printf hello"}
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
