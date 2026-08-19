package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/lgxz/dora"
)

// GlobTool finds files matching a glob pattern.
type GlobTool struct{}

// NewGlobTool creates a glob tool.
func NewGlobTool() *GlobTool { return &GlobTool{} }

// defaultGlobMaxResults is the default max results when not given.
const defaultGlobMaxResults = 100

// Spec implements dora.Tool.
func (t *GlobTool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "Glob",
		Description: "Find files matching a glob pattern. Use for precise file discovery",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Glob pattern (supports ** for recursive matching, e.g. **/*.py)"},
    "path": {"type": "string", "description": "Directory to search. Default current dir."},
    "max_results": {"type": "integer", "minimum": 1, "description": "Max results. Default 100."}
  },
  "required": ["pattern"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *GlobTool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	input, err := decodeGlobInput(raw)
	if err != nil {
		return dora.ToolResult{}, err
	}

	root := input.Path
	if root == "" {
		root = "."
	}
	info, err := os.Stat(root)
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("glob: %w", err)
	}
	if !info.IsDir() {
		return dora.ToolResult{}, fmt.Errorf("glob: %s is not a directory", root)
	}

	maxResults := defaultGlobMaxResults
	if input.MaxResults != nil {
		maxResults = *input.MaxResults
	}
	var sb strings.Builder
	count := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && ignoreDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		// Match the pattern against the path relative to the search root so
		// patterns like *.py work regardless of the root's absolute location.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		// doublestar.Match hardcodes '/' as the path separator, so normalize
		// the OS-specific separators (e.g. '\' on Windows) to '/'.
		rel = filepath.ToSlash(rel)
		matched, err := doublestar.Match(input.Pattern, rel)
		if err != nil {
			return nil
		}
		if matched {
			fmt.Fprintf(&sb, "%s\n", path)
			count++
			if count >= maxResults {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("glob: walk: %w", err)
	}
	if count == 0 {
		return dora.ToolResult{Content: "(no matches)\n"}, nil
	}
	return dora.ToolResult{Content: sb.String()}, nil
}

type globInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	MaxResults *int   `json:"max_results,omitempty"`
}

func decodeGlobInput(raw json.RawMessage) (globInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value globInput
	if err := decoder.Decode(&value); err != nil {
		return globInput{}, fmt.Errorf("glob: decode input: %w", err)
	}
	if value.Pattern == "" {
		return globInput{}, errors.New("glob: pattern is required")
	}
	if value.MaxResults != nil && *value.MaxResults < 1 {
		return globInput{}, errors.New("glob: max_results must be positive")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return globInput{}, errors.New("glob: input must contain one JSON value")
		}
		return globInput{}, fmt.Errorf("glob: decode input: %w", err)
	}
	return value, nil
}

var _ dora.Tool = (*GlobTool)(nil)
