// Package imageutil centralizes image handling for Dora: parsing @@path@@
// tags from tool output, validating image files, and converting image files
// to data URLs for model adapters.
package imageutil

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// MaxImageBytes bounds the size of an image file that can be attached. Larger
// images would consume excessive request body and model context.
const MaxImageBytes = 4 << 20

// tagPattern matches a @@path@@ tag and captures the file path. The @@
// delimiter is distinctive and survives JSON encoding unchanged, so it is
// unlikely to collide with ordinary command output.
var tagPattern = regexp.MustCompile(`@@([^@]+)@@`)

// ParseTags extracts every @@path@@ tag from text. It returns the paths whose
// files are valid (exist, are images, and are within the size limit) and a
// human-readable note for every tag whose file cannot be attached, so the
// model can correct the path and retry.
func ParseTags(text string) (paths []string, notes []string) {
	matches := tagPattern.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	for _, match := range matches {
		path := strings.TrimSpace(match[1])
		if path == "" {
			continue
		}
		if err := Validate(path); err != nil {
			notes = append(notes, fmt.Sprintf("image %q could not be attached: %v", path, err))
			continue
		}
		paths = append(paths, path)
	}
	return paths, notes
}

// Validate checks that a path exists, is an image, and is within the size
// limit.
func Validate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > MaxImageBytes {
		return fmt.Errorf("exceeds %d bytes", MaxImageBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return fmt.Errorf("not an image (detected %q)", mime)
	}
	return nil
}

// DataURL reads an image file and returns it as a data URL with a sniffed mime
// type. The file must exist, be an image, and not exceed MaxImageBytes.
func DataURL(path string) (string, error) {
	if path == "" {
		return "", errors.New("image path is empty")
	}
	if err := Validate(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read image %q: %w", path, err)
	}
	mime := http.DetectContentType(data)
	encoded := base64.StdEncoding.EncodeToString(data)
	return "data:" + mime + ";base64," + encoded, nil
}