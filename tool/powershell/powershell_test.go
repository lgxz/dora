package powershell

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lgxz/dora/internal/job"
)

func TestNewReportsUnavailableExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := New(Config{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteReturnsCommandOutput(t *testing.T) {
	tool, err := New(Config{JobManager: job.New()})
	if errors.Is(err, ErrUnavailable) {
		t.Skip("PowerShell is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}

	toolResult, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"Write-Output 'hello'"}`))

	output := toolResult.Content
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != "hello" {
		t.Fatalf("output = %q, want plain stdout", output)
	}
}

func TestSpecIdentifiesPowerShell(t *testing.T) {
	tool, err := New(Config{JobManager: job.New()})
	if errors.Is(err, ErrUnavailable) {
		t.Skip("PowerShell is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	spec := tool.Spec()
	if spec.Name != "powershell" ||
		!strings.Contains(spec.Description, "Execute PowerShell command.") ||
		!json.Valid(spec.InputSchema) || !strings.Contains(string(spec.InputSchema), "wait_seconds") {
		t.Fatalf("spec = %#v", spec)
	}
}

func TestCommandArgsUseNonInteractivePowerShell(t *testing.T) {
	want := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Get-Process"}
	if got := commandArgs("Get-Process"); !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestExecuteOnNilTool(t *testing.T) {
	var tool *Tool
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"Get-Date"}`))
	if err == nil || err.Error() != "powershell: tool is not initialized" {
		t.Fatalf("error = %v", err)
	}
}
