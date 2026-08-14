// Package file implements read/write/edit tools for precise file operations.
package file

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lgxz/dora"
)

const maxReadBytes = 1 << 20 // 1MB

// ReadTool reads a file with line numbers.
type ReadTool struct{}

// NewReadTool creates a read tool.
func NewReadTool() *ReadTool { return &ReadTool{} }

// defaultReadLimit is the default max lines to read when limit is not given.
const defaultReadLimit = 200

// Spec implements dora.Tool.
func (t *ReadTool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "read",
		Description: "Read a file with line numbers.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path to read"},
    "offset": {"type": "integer", "minimum": 1, "description": "Start line (1-based). Default 1."},
    "limit": {"type": "integer", "minimum": 1, "description": "Max lines to read. Default 200."}
  },
  "required": ["path"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *ReadTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	input, err := decodeReadInput(raw)
	if err != nil {
		return "", err
	}
	f, err := os.Open(input.Path)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	defer f.Close()

	// Detect binary by reading the first chunk without loading the whole file.
	head := make([]byte, 1024)
	n, _ := io.ReadFull(f, head)
	if isBinary(head[:n]) {
		return "(binary file; use the bash tool to inspect it)", nil
	}
	// Rewind to the start for line scanning.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("read: seek: %w", err)
	}

	// Stream lines, collecting the requested range. Stop after the range so
	// large files are not fully read.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxReadBytes)
	var sb strings.Builder
	start := input.Offset
	limit := defaultReadLimit
	if input.Limit != nil {
		limit = *input.Limit
	}
	end := start + limit - 1
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if lineNo >= start && lineNo <= end {
			// Prefix each line with its number for reference in edit operations.
			fmt.Fprintf(&sb, "%d: %s\n", lineNo, scanner.Text())
		}
		if lineNo >= end {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read: scan: %w", err)
	}
	if lineNo == 0 {
		return "(empty file)", nil
	}
	if sb.Len() == 0 {
		return fmt.Sprintf("(offset %d is beyond the file's %d lines)", start, lineNo), nil
	}
	return sb.String(), nil
}

type readInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

func decodeReadInput(raw json.RawMessage) (readInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value readInput
	if err := decoder.Decode(&value); err != nil {
		return readInput{}, fmt.Errorf("read: decode input: %w", err)
	}
	if value.Path == "" {
		return readInput{}, errors.New("read: path is required")
	}
	if value.Offset == 0 {
		value.Offset = 1 // default to first line (1-based)
	}
	if value.Offset < 1 {
		return readInput{}, errors.New("read: offset must be positive (1-based)")
	}
	if value.Limit != nil && *value.Limit < 1 {
		return readInput{}, errors.New("read: limit must be positive")
	}
	if err := ensureSingleValue(decoder); err != nil {
		return readInput{}, err
	}
	return value, nil
}

// WriteTool writes content to a file.
type WriteTool struct{}

// NewWriteTool creates a write tool.
func NewWriteTool() *WriteTool { return &WriteTool{} }

// Spec implements dora.Tool.
func (t *WriteTool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "write",
		Description: "Write content to a file.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path"},
    "content": {"type": "string", "description": "Content to write"},
    "append": {"type": "boolean", "description": "Append(without adding newline) instead of overwrite. Default false."}
  },
  "required": ["path", "content"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *WriteTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	input, err := decodeWriteInput(raw)
	if err != nil {
		return "", err
	}
	created := false
	if _, err := os.Stat(input.Path); os.IsNotExist(err) {
		created = true
	}
	if err := writeFile(input.Path, []byte(input.Content), input.Append); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return fmt.Sprintf("bytes_written: %d, created: %v", len(input.Content), created), nil
}

type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Append  bool   `json:"append,omitempty"`
}

func decodeWriteInput(raw json.RawMessage) (writeInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value writeInput
	if err := decoder.Decode(&value); err != nil {
		return writeInput{}, fmt.Errorf("write: decode input: %w", err)
	}
	if value.Path == "" {
		return writeInput{}, errors.New("write: path is required")
	}
	if err := ensureSingleValue(decoder); err != nil {
		return writeInput{}, err
	}
	return value, nil
}

// EditTool edits a file by exact string replacement.
type EditTool struct{}

// NewEditTool creates an edit tool.
func NewEditTool() *EditTool { return &EditTool{} }

// Spec implements dora.Tool.
func (t *EditTool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "edit",
		Description: "Edit a file by exact string replacement.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path"},
    "old_string": {"type": "string", "description": "Exact text to find and replace"},
    "new_string": {"type": "string", "description": "Replacement text"},
    "replace_all": {"type": "boolean", "description": "Replace all occurrences. Default false."}
  },
  "required": ["path", "old_string", "new_string"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *EditTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	input, err := decodeEditInput(raw)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(input.Path)
	if err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	content := string(data)
	var newContent string
	var count int
	if input.ReplaceAll {
		newContent = strings.ReplaceAll(content, input.OldString, input.NewString)
		count = strings.Count(content, input.OldString)
	} else {
		newContent = strings.Replace(content, input.OldString, input.NewString, 1)
		if newContent != content {
			count = 1
		}
	}
	if count == 0 {
		return "old_string not found in file", nil
	}
	if err := writeFile(input.Path, []byte(newContent), false); err != nil {
		return "", fmt.Errorf("edit: %w", err)
	}
	bytesChanged := len(newContent) - len(content)
	return fmt.Sprintf("replacements: %d, bytes_changed: %d", count, bytesChanged), nil
}

type editInput struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func decodeEditInput(raw json.RawMessage) (editInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value editInput
	if err := decoder.Decode(&value); err != nil {
		return editInput{}, fmt.Errorf("edit: decode input: %w", err)
	}
	if value.Path == "" {
		return editInput{}, errors.New("edit: path is required")
	}
	if value.OldString == "" {
		return editInput{}, errors.New("edit: old_string is required")
	}
	if err := ensureSingleValue(decoder); err != nil {
		return editInput{}, err
	}
	return value, nil
}

// --- shared helpers ---

func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	// Heuristic: a high ratio of non-printable bytes suggests binary.
	nonPrintable := 0
	sample := data
	if len(sample) > 1024 {
		sample = sample[:1024]
	}
	for _, b := range sample {
		if b < 0x09 || (b > 0x0d && b < 0x20) || b == 0x7f {
			nonPrintable++
		}
	}
	return nonPrintable > len(sample)/10
}

func writeFile(path string, data []byte, appendMode bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if appendMode {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func ensureSingleValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("input must contain one JSON value")
		}
		return fmt.Errorf("decode input: %w", err)
	}
	return nil
}

var (
	_ dora.Tool = (*ReadTool)(nil)
	_ dora.Tool = (*WriteTool)(nil)
	_ dora.Tool = (*EditTool)(nil)
)
