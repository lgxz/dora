// Package viewimage implements a dora.Tool that attaches a local image file or
// remote image URL to a tool result for a vision-capable model.
package viewimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/imagefile"
)

// Tool loads an image from a local path or a remote URL for viewing.
type Tool struct{}

// New creates a view_image tool.
func New() *Tool { return &Tool{} }

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "view_image",
		Description: "Load an image from a local file path or a remote URL so a vision-capable model can view it.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Local image file path (mutually exclusive with url)"},
    "url": {"type": "string", "description": "Remote image URL (mutually exclusive with path)"}
  },
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	input, err := decodeInput(raw)
	if err != nil {
		return dora.ToolResult{}, err
	}

	switch {
	case input.Path != "":
		path := expandHome(input.Path)
		if err := imagefile.Validate(path); err != nil {
			return dora.ToolResult{}, fmt.Errorf("view_image: %w", err)
		}
		return dora.ToolResult{
			Content: fmt.Sprintf("loaded image %q", path),
			Images:  []dora.Image{{Path: path}},
		}, nil

	default:
		return dora.ToolResult{
			Content: fmt.Sprintf("loaded image %q", input.URL),
			Images:  []dora.Image{{URL: input.URL}},
		}, nil
	}
}

// expandHome expands a leading "~" (or "~/") into the current user's home
// directory. Only a bare "~" or a "~/" prefix is expanded; "~user", or a "~"
// appearing anywhere else in the path, is left untouched. When the home
// directory cannot be determined, the original path is returned unchanged so
// the subsequent validation reports the natural "file not found" error.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

type input struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

func decodeInput(raw json.RawMessage) (input, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value input
	if err := decoder.Decode(&value); err != nil {
		return input{}, fmt.Errorf("view_image: decode input: %w", err)
	}
	if err := ensureSingleValue(decoder); err != nil {
		return input{}, err
	}

	value.Path = strings.TrimSpace(value.Path)
	value.URL = strings.TrimSpace(value.URL)
	if value.Path == "" && value.URL == "" {
		return input{}, errors.New("view_image: exactly one of path or url is required")
	}
	if value.Path != "" && value.URL != "" {
		return input{}, errors.New("view_image: path and url are mutually exclusive")
	}
	if value.URL != "" {
		u, err := url.Parse(value.URL)
		if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
			return input{}, errors.New("view_image: url must be an absolute http(s) URL")
		}
	}
	return value, nil
}

func ensureSingleValue(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("view_image: input must contain one JSON value")
		}
		return fmt.Errorf("view_image: decode input: %w", err)
	}
	return nil
}

var _ dora.Tool = (*Tool)(nil)
