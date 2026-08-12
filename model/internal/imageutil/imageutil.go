// Package imageutil provides shared image-to-data-URL conversion for model
// adapters.
package imageutil

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/lgxz/dora"
)

// DataURL resolves an image reference to a data URL. A Path is read from disk
// and base64-encoded with a sniffed mime type; a URL is used directly.
func DataURL(image dora.Image) (string, error) {
	if image.URL != "" {
		return image.URL, nil
	}
	if image.Path == "" {
		return "", errors.New("image has neither Path nor URL")
	}
	data, err := os.ReadFile(image.Path)
	if err != nil {
		return "", fmt.Errorf("read image %q: %w", image.Path, err)
	}
	mime := http.DetectContentType(data)
	encoded := base64.StdEncoding.EncodeToString(data)
	return "data:" + mime + ";base64," + encoded, nil
}