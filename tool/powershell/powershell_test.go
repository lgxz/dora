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
		t.Fatalf("error = %v, want ErrUnavailable", err)
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
	var result commandResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || strings.TrimSpace(result.Stdout) != "hello" || result.Stderr != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestSpecIdentifiesPowerShell(t *testing.T) {
	tool := &Tool{timeout: 30 * time.Second}
	spec := tool.Spec()
	if spec.Name != "powershell" || !strings.Contains(spec.Description, "PowerShell") ||
		!json.Valid(spec.InputSchema) || !strings.Contains(string(spec.InputSchema), "default of 30s") {
		t.Fatalf("spec = %#v", spec)
	}
}

func TestCommandArgsUseNonInteractivePowerShell(t *testing.T) {
	want := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "Get-Process"}
	if got := commandArgs("Get-Process"); !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
}

func TestDecodeInputRejectsUnknownField(t *testing.T) {
	_, err := decodeInput(json.RawMessage(`{"command":"Get-Date","extra":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

func TestDecodeInputAcceptsTimeout(t *testing.T) {
	value, err := decodeInput(json.RawMessage(`{"command":"Get-Date","timeout_seconds":300}`))
	if err != nil {
		t.Fatal(err)
	}
	if value.TimeoutSeconds == nil || *value.TimeoutSeconds != 300 {
		t.Fatalf("timeout = %#v", value.TimeoutSeconds)
	}
}

func TestDecodeInputRejectsInvalidTimeout(t *testing.T) {
	for _, raw := range []string{
		`{"command":"Get-Date","timeout_seconds":0}`,
		`{"command":"Get-Date","timeout_seconds":3601}`,
	} {
		if _, err := decodeInput(json.RawMessage(raw)); err == nil {
			t.Fatalf("decodeInput(%s) succeeded", raw)
		}
	}
}

func TestLimitedBufferTruncates(t *testing.T) {
	buffer := newLimitedBuffer(4)
	if _, err := buffer.Write([]byte("123456")); err != nil {
		t.Fatal(err)
	}
	if buffer.String() != "1234" || !buffer.Truncated() {
		t.Fatalf("buffer = %q, truncated = %t", buffer.String(), buffer.Truncated())
	}
}
