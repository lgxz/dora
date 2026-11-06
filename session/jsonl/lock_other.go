//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package jsonl

import "os"

// openExclusive opens or creates path. This platform has no file locking, so
// callers must not open one session from two processes.
func openExclusive(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
}
