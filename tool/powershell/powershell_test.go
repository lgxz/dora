package powershell

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewReportsUnavailableExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := New(Config{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteReturnsCommandOutput(t *testing.T) {
	tool, err := New(Config{})
	if errors.Is(err, ErrUnavailable) {
		t.Skip("PowerShell is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"Write-Output 'hello'"}`))
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
	if result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != "hello" || result.Stderr != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestSpecIdentifiesPowerShell(t *testing.T) {
	tool, err := New(Config{Timeout: 120 * time.Second})
	if errors.Is(err, ErrUnavailable) {
		t.Skip("PowerShell is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	spec := tool.Spec()
	if spec.Name != "powershell" ||
		!strings.Contains(spec.Description, "Execute PowerShell command.") ||
		!strings.Contains(spec.Description, "To load an image file for viewing, use `echo @@path@@`") ||
		!json.Valid(spec.InputSchema) || !strings.Contains(string(spec.InputSchema), "default of 2m0s") {
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
