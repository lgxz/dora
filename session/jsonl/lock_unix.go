//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package jsonl

import (
	"errors"
	"os"
	"syscall"
)

// openExclusive opens or creates path and takes an exclusive advisory lock
// that lasts until the file is closed.
func openExclusive(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errInUse
		}
		return nil, err
	}
	return file, nil
}
