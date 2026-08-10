package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lgxz/dora"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	messages := []dora.Message{
		{Role: dora.RoleUser, Content: "hello"},
		{
			Role: dora.RoleAssistant,
			ToolCalls: []dora.ToolCall{{
				ID:    "call-1",
				Name:  "bash",
				Input: json.RawMessage(`{"command":"pwd"}`),
			}},
		},
		{Role: dora.RoleTool, ToolCallID: "call-1", Content: "result"},
		{Role: dora.RoleAssistant, Content: "done"},
	}

	backend := Backend{Provider: "openai", API: "responses", Model: "gpt-test", BaseURL: "https://example.test/v1"}
	if err := store.Save("system-status", 0, Snapshot{
		Backend:      backend,
		Messages:     messages,
		Continuation: "opaque-state",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load("system-status")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || snapshot.Backend != backend || snapshot.Continuation != "opaque-state" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if !jsonEqual(snapshot.Messages[1].ToolCalls[0].Input, messages[1].ToolCalls[0].Input) {
		t.Fatalf("tool input = %s", snapshot.Messages[1].ToolCalls[0].Input)
	}
	snapshot.Messages[1].ToolCalls[0].Input = nil
	messages[1].ToolCalls[0].Input = nil
	if !reflect.DeepEqual(snapshot.Messages, messages) {
		t.Fatalf("messages = %#v, want %#v", snapshot.Messages, messages)
	}

	info, err := os.Stat(filepath.Join(dir, "system-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func jsonEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	return json.Unmarshal(left, &leftValue) == nil &&
		json.Unmarshal(right, &rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}

func TestStoreDetectsRevisionConflict(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	next := Snapshot{Backend: Backend{Provider: "openai", API: "chat_completions", Model: "test", BaseURL: "http://localhost"}}
	if err := store.Save("task", 0, next); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("task", 0, next); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
}

func TestStoreRejectsUnsafeName(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Load("../escape")
	if err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte(`{"version":3,"revision":1,"backend":{"provider":"openai","api":"chat_completions","model":"test","base_url":"http://localhost"},"messages":[],"extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Load("broken")
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRejectsVersionOne(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"revision":1,"messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Load("old")
	if err == nil || !strings.Contains(err.Error(), "unsupported version 1") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRejectsVersionTwo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"revision":1,"backend":{"provider":"openai-responses","model":"gpt-test","base_url":"https://example.test/v1"},"messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Load("old")
	if err == nil || !strings.Contains(err.Error(), "unsupported version 2") {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreCanReplaceVersionOneByRevision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"revision":7,"messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := store.Revision("old")
	if err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("revision = %d", revision)
	}
	if err := store.Save("old", revision, Snapshot{
		Backend:  Backend{Provider: "openai", API: "chat_completions", Model: "new", BaseURL: "http://localhost"},
		Messages: []dora.Message{{Role: dora.RoleUser, Content: "replacement"}},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load("old")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 8 || snapshot.Messages[0].Content != "replacement" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}
