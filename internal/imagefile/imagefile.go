// Package imagefile validates and encodes local image files used by tools and
// model providers.
package imagefile

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// MaxBytes bounds the size of an attached image file.
const MaxBytes = 4 << 20

// Validate checks that path exists, is an image, and is within the size limit.
func Validate(path string) error {
	if path == "" {
		return errors.New("image path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > MaxBytes {
		return fmt.Errorf("exceeds %d bytes", MaxBytes)
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

// DataURL reads a validated image and returns its data URL representation.
func DataURL(path string) (string, error) {
	if err := Validate(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read image %q: %w", path, err)
	}
	mime := http.DetectContentType(data)
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
