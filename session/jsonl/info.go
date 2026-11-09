package jsonl

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// infoBufferSize bounds how much of a session file ReadInfo looks at per line.
// A longer line is not parsed.
const infoBufferSize = 64 << 10

// Info is what the first lines of a session file reveal. It is read without
// taking the file's lock, so it also works for a session another process is
// using.
type Info struct {
	Path string
	// Workdir is the directory the session was created for. It is empty for
	// files that predate the header field.
	Workdir string
	// ModTime is the file's last modification time, the best sign of when the
	// session was last used.
	ModTime time.Time
	// Prompt is the user prompt of the first turn. It is empty when the file
	// holds no turn yet, or when the first record is too long to read here.
	Prompt string
	// Empty reports that the file holds a header and nothing else.
	Empty bool
}

// ReadInfo reads the header and the first turn of the session file at path.
func ReadInfo(path string) (Info, error) {
	file, err := os.Open(path)
	if err != nil {
		return Info{}, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return Info{}, err
	}
	if !stat.Mode().IsRegular() {
		return Info{}, fmt.Errorf("session %q is not a regular file", path)
	}

	reader := bufio.NewReaderSize(file, infoBufferSize)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return Info{}, errNotSession
	}
	var header headerRec
	if json.Unmarshal(line, &header) != nil || header.T != recHeader {
		return Info{}, errNotSession
	}
	if header.V != formatVersion {
		return Info{}, fmt.Errorf("unsupported session format version %d", header.V)
	}
	info := Info{Path: path, Workdir: header.Workdir, ModTime: stat.ModTime()}

	line, err = reader.ReadSlice('\n')
	switch {
	case errors.Is(err, io.EOF) && len(line) == 0:
		info.Empty = true
	case errors.Is(err, bufio.ErrBufferFull):
		// A very long first record: it exists, but is not parsed.
	case err == nil:
		var record struct {
			T      string `json:"t"`
			Parent int64  `json:"parent"`
			User   string `json:"user"`
		}
		if json.Unmarshal(line, &record) == nil && record.T == recTurn && record.Parent == 0 {
			info.Prompt = record.User
		}
	}
	return info, nil
}
