package file

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/lgxz/dora"
)

// GrepTool searches file contents.
type GrepTool struct {
	rgOnce sync.Once
	rgPath string
}

// NewGrepTool creates a grep tool.
func NewGrepTool() *GrepTool { return &GrepTool{} }

// defaultGrepMaxResults is the default max results when not given.
const defaultGrepMaxResults = 100

// Spec implements dora.Tool.
func (t *GrepTool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "grep",
		Description: "Search file contents",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Text to search for"},
    "path": {"type": "string", "description": "File or directory to search"},
    "regex": {"type": "boolean", "description": "Use regex matching. Default false."},
    "ignore_case": {"type": "boolean", "description": "Case-insensitive. Default false."},
    "max_results": {"type": "integer", "minimum": 1, "description": "Max results. Default 100."}
  },
  "required": ["pattern", "path"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *GrepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	input, err := decodeGrepInput(raw)
	if err != nil {
		return "", err
	}

	// Prefer ripgrep (respects .gitignore, skips hidden/binary files). Fall
	// back to the built-in walker if rg is unavailable. The rg availability
	// check is cached so it only runs once.
	t.rgOnce.Do(func() {
		t.rgPath, _ = exec.LookPath("rg")
	})
	if t.rgPath != "" {
		return t.executeWithRG(ctx, input)
	}
	return t.executeFallback(ctx, input)
}

// executeWithRG searches using ripgrep.
func (t *GrepTool) executeWithRG(ctx context.Context, input grepInput) (string, error) {
	args := []string{"--line-number", "--no-heading"}
	if input.Regex {
		args = append(args, "--regexp", input.Pattern)
	} else {
		args = append(args, "--fixed-strings", input.Pattern)
	}
	if input.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if input.MaxResults != nil {
		args = append(args, "--max-count", strconv.Itoa(*input.MaxResults))
	}
	args = append(args, input.Path)

	cmd := exec.CommandContext(ctx, "rg", args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "(no matches)\n", nil
		}
		return "", fmt.Errorf("grep: rg: %w", err)
	}
	return string(out), nil
}

// executeFallback searches using the built-in walker (no rg dependency).
func (t *GrepTool) executeFallback(ctx context.Context, input grepInput) (string, error) {
	// Compile the regex (or use literal matching).
	var re *regexp.Regexp
	if input.Regex {
		pattern := input.Pattern
		if input.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("grep: invalid regex: %w", err)
		}
	}

	// Collect files to search (file or directory), skipping common ignore dirs.
	var files []string
	singleFile := false
	info, err := os.Stat(input.Path)
	if err != nil {
		return "", fmt.Errorf("grep: %w", err)
	}
	if info.IsDir() {
		err = filepath.WalkDir(input.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && ignoreDir(d.Name()) {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("grep: walk: %w", err)
		}
	} else {
		files = []string{input.Path}
		singleFile = true
	}

	// Search each file line by line.
	maxResults := defaultGrepMaxResults
	if input.MaxResults != nil {
		maxResults = *input.MaxResults
	}
	var sb strings.Builder
	count := 0
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), maxReadBytes)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			var matched bool
			if input.Regex {
				matched = re.MatchString(line)
			} else if input.IgnoreCase {
				matched = strings.Contains(strings.ToLower(line), strings.ToLower(input.Pattern))
			} else {
				matched = strings.Contains(line, input.Pattern)
			}
			if matched {
				if singleFile {
					fmt.Fprintf(&sb, "%d:%s\n", lineNo, line)
				} else {
					fmt.Fprintf(&sb, "%s:%d:%s\n", file, lineNo, line)
				}
				count++
				if count >= maxResults {
					break
				}
			}
		}
		if err := scanner.Err(); err != nil {
			f.Close()
			return "", fmt.Errorf("grep: scan %s: %w", file, err)
		}
		f.Close()
		if count >= maxResults {
			break
		}
	}
	if count == 0 {
		return "(no matches)\n", nil
	}
	return sb.String(), nil
}

// ignoreDir reports whether a directory should be skipped during search.
func ignoreDir(name string) bool {
	switch name {
	case ".git", ".venv", "venv", "node_modules", "__pycache__",
		".idea", ".vscode", "dist", "build", ".tox", ".mypy_cache",
		".pytest_cache", ".cache", "target":
		return true
	}
	return false
}

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Regex      bool   `json:"regex,omitempty"`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
	MaxResults *int   `json:"max_results,omitempty"`
}

func decodeGrepInput(raw json.RawMessage) (grepInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value grepInput
	if err := decoder.Decode(&value); err != nil {
		return grepInput{}, fmt.Errorf("grep: decode input: %w", err)
	}
	if value.Pattern == "" {
		return grepInput{}, errors.New("grep: pattern is required")
	}
	if value.Path == "" {
		return grepInput{}, errors.New("grep: path is required")
	}
	if value.MaxResults != nil && *value.MaxResults < 1 {
		return grepInput{}, errors.New("grep: max_results must be positive")
	}
	if err := ensureSingleValue(decoder); err != nil {
		return grepInput{}, err
	}
	return value, nil
}

var _ dora.Tool = (*GrepTool)(nil)
