package bash

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecuteReturnsCommandOutput(t *testing.T) {
	tool, err := New(Config{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"printf hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if result.ExitCode != 0 || result.Stdout != "hello" || result.Stderr != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteReturnsNonzeroExitToModel(t *testing.T) {
	tool, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"printf problem >&2; exit 7"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if result.ExitCode != 7 || result.Stderr != "problem" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteTimesOut(t *testing.T) {
	tool, err := New(Config{Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"sleep 1"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if !result.TimedOut || result.ExitCode != -1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteTruncatesOutput(t *testing.T) {
	tool, err := New(Config{MaxOutputBytes: 4})
	if err != nil {
		t.Fatal(err)
	}

	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"printf 123456"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, output)
	if !result.Truncated || result.Stdout != "1234" {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteRejectsUnknownInput(t *testing.T) {
	tool, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = tool.Execute(context.Background(), json.RawMessage(`{"command":"true","extra":1}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteHonorsCancellation(t *testing.T) {
	tool, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = tool.Execute(ctx, json.RawMessage(`{"command":"echo no"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func decodeResult(t *testing.T, output string) commandResult {
	t.Helper()
	var result commandResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
