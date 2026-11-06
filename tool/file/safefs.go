package file

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// This file holds the machinery that makes write and edit safe when the Agent
// runs several tool calls at once and when a run is interrupted mid-write:
//
//   - pathLocks serializes whole read-modify-write cycles on one file;
//   - writeFileAtomic never exposes a truncated or half-written file;
//   - errFileChanged reports an external modification noticed before commit.
//
// It does not order calls relative to each other (that is the Agent's job) and
// it does not make bash or other processes honor the in-process lock.

// errFileChanged is returned when the file was modified by something other
// than this call between reading it and committing the new content.
var errFileChanged = errors.New("file changed on disk while it was being edited; re-read it and retry")

// beforeCommit is a test hook invoked after the new content is computed and
// before the file is checked for external changes and replaced.
var beforeCommit func()

// lockEntry is one lock in the registry, shared by every path that resolves to
// the same file.
type lockEntry struct {
	mu       sync.Mutex
	resolved string      // symlink-resolved path, used while the file may not exist
	info     fs.FileInfo // identity of the file when the entry was created, if it existed
	refs     int
}

var pathLocks struct {
	mu      sync.Mutex
	entries []*lockEntry
}

// lockPath acquires the lock for the file at path and returns its release
// function. Paths are matched by file identity (os.SameFile), so a symlink, a
// hard link, or a differently-cased spelling on a case-insensitive file system
// shares one lock with the file itself; for a file that does not exist yet the
// symlink-resolved path is compared instead. Entries are reference counted and
// dropped when unused, so the registry does not grow over a long session.
func lockPath(path string) (unlock func()) {
	resolved := resolveForLock(path)
	info, _ := os.Stat(resolved)

	pathLocks.mu.Lock()
	var entry *lockEntry
	for _, candidate := range pathLocks.entries {
		if candidate.resolved == resolved ||
			(info != nil && candidate.info != nil && os.SameFile(info, candidate.info)) {
			entry = candidate
			break
		}
	}
	if entry == nil {
		entry = &lockEntry{resolved: resolved, info: info}
		pathLocks.entries = append(pathLocks.entries, entry)
	}
	entry.refs++
	pathLocks.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		pathLocks.mu.Lock()
		defer pathLocks.mu.Unlock()
		entry.refs--
		if entry.refs == 0 {
			for i, candidate := range pathLocks.entries {
				if candidate == entry {
					pathLocks.entries = append(pathLocks.entries[:i], pathLocks.entries[i+1:]...)
					break
				}
			}
		}
	}
}

// resolveForLock returns an absolute path with symlinks resolved as far as the
// file system allows. A missing final component is kept as is.
func resolveForLock(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// readFileSnapshot reads the whole file from one open handle and returns the
// handle's metadata taken before the read, so unchanged() can later tell
// whether the file moved on in the meantime.
func readFileSnapshot(path string) ([]byte, fs.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

// unchanged reports whether path still names the same file with the same size
// and modification time as before.
func unchanged(path string, before fs.FileInfo) bool {
	now, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(now, before) && now.Size() == before.Size() && now.ModTime().Equal(before.ModTime())
}

// writeFileAtomic replaces path with data so that a reader, a crash, a full
// disk, or a kill mid-write sees either the complete old content or the
// complete new content, never a truncated file. It writes a temporary file in
// the same directory, syncs it, and renames it over the target.
//
// A symlink is followed, so the link itself stays a link. The existing
// permission bits are kept. Ownership and hard links are not preserved (the
// target becomes a new file). When atomic replacement is impossible (the
// target is not a regular file, or the directory is not writable) it falls
// back to writing in place, as the tools did before.
func writeFileAtomic(path string, data []byte) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	mode := fs.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		if !info.Mode().IsRegular() {
			return os.WriteFile(target, data, 0o644)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return os.WriteFile(target, data, mode)
		}
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(target), err)
	}
	committed = true
	return nil
}
