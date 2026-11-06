//go:build windows

package jsonl

import (
	"errors"
	"os"
	"syscall"
)

// errorSharingViolation is ERROR_SHARING_VIOLATION, which package syscall does
// not name.
const errorSharingViolation syscall.Errno = 32

// openExclusive opens or creates path without write sharing, so a second
// process cannot open it for writing while this one holds it.
func openExclusive(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errorSharingViolation) {
			return nil, errInUse
		}
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
