package dora

import "context"

type workingDirectoryContextKey struct{}

func withWorkingDirectory(ctx context.Context, directory string) context.Context {
	return context.WithValue(ctx, workingDirectoryContextKey{}, directory)
}

// WorkingDirectory returns the per-run directory used to resolve relative
// paths. An empty result means callers should use their normal process working
// directory behavior.
func WorkingDirectory(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	directory, _ := ctx.Value(workingDirectoryContextKey{}).(string)
	return directory
}
