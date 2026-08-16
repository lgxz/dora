package provider

import (
	"errors"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/imagefile"
)

// ImageDataURL resolves an image reference to a data URL. A URL is used
// directly; a Path is read and encoded from the local filesystem. The returned error is
// unprefixed; callers wrap it with their provider label.
func ImageDataURL(image dora.Image) (string, error) {
	if image.URL != "" {
		return image.URL, nil
	}
	if image.Path == "" {
		return "", errors.New("image has neither Path nor URL")
	}
	return imagefile.DataURL(image.Path)
}
