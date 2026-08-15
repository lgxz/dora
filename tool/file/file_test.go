package file

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "1:line1\n2:line2\n3:line3\n"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

func TestReadToolRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("l1\nl2\nl3\nl4\nl5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","offset":2,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "2:l2\n3:l3\n"
	if out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

func TestReadToolBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.dat")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "binary") {
		t.Fatalf("expected binary notice, got %q", out)
	}
}

func TestReadToolMissingFile(t *testing.T) {
	tool := NewReadTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"/nonexistent/file.txt"}`))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestReadToolEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "empty") {
		t.Fatalf("expected empty file notice, got %q", out)
	}
}

func TestReadToolOffsetBeyondFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("l1\nl2\nl3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewReadTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","offset":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "beyond") {
		t.Fatalf("expected beyond notice, got %q", out)
	}
}

func TestWriteTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "test.txt")
	tool := NewWriteTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","content":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "bytes_written: 5") || !strings.Contains(out, "created: true") {
		t.Fatalf("out = %q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q", data)
	}
}

func TestWriteToolAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewWriteTool()
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","content":"+more","append":true}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "base+more" {
		t.Fatalf("content = %q", data)
	}
}

func TestEditTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("foo bar foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","old_string":"foo","new_string":"baz"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "replacements: 1") {
		t.Fatalf("out = %q", out)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "baz bar foo" {
		t.Fatalf("content = %q", data)
	}
}

func TestEditToolReplaceAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("foo bar foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","old_string":"foo","new_string":"baz","replace_all":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "replacements: 2") {
		t.Fatalf("out = %q", out)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "baz bar baz" {
		t.Fatalf("content = %q", data)
	}
}

func TestEditToolNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`","old_string":"missing","new_string":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not found") {
		t.Fatalf("expected not found, got %q", out)
	}
}
