// Package runpath resolves tool paths against the current Agent run.
package runpath

import (
	"context"
	"path/filepath"

	"github.com/lgxz/dora"
)

// Resolve returns path unchanged when it is absolute or no per-run working
// directory is set. Otherwise it resolves path against that directory.
func Resolve(ctx context.Context, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	directory := dora.WorkingDirectory(ctx)
	if directory == "" {
		return path
	}
	return filepath.Join(directory, path)
}
