// Package imageoutput converts the command tools' @@path@@ convention into a
// structured dora.ToolResult.
package imageoutput

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/imagefile"
)

var tagPattern = regexp.MustCompile(`@@([^@]+)@@`)

// Parse extracts valid image paths from content. Invalid paths remain in the
// content and receive a note so the model can correct them and retry.
func Parse(content string) dora.ToolResult {
	result := dora.ToolResult{Content: content}
	var notes []string
	for _, match := range tagPattern.FindAllStringSubmatch(content, -1) {
		path := strings.TrimSpace(match[1])
		if path == "" {
			continue
		}
		if err := imagefile.Validate(path); err != nil {
			notes = append(notes, fmt.Sprintf("image %q could not be attached: %v", path, err))
			continue
		}
		result.Images = append(result.Images, dora.Image{Path: path})
	}
	if len(notes) > 0 {
		result.Content += "\n\n" + strings.Join(notes, "\n")
	}
	return result
}
