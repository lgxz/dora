// Package session persists named Dora conversations as atomic JSON snapshots.
package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"dora"
)

const (
	fileVersion = 3
	maxFileSize = 64 << 20
)

var (
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

	// ErrConflict indicates that a session changed after it was loaded.
	ErrConflict = errors.New("session revision conflict")

	// ErrUnsupportedVersion indicates that a session must be replaced before
	// it can be used by this version of Dora.
	ErrUnsupportedVersion = errors.New("unsupported version")
)

// Snapshot is one committed version of a named conversation.
type Snapshot struct {
	Revision     uint64
	Backend      Backend
	Messages     []dora.Message
	Continuation string
}

// Backend identifies the model endpoint that owns an opaque continuation.
type Backend struct {
	Provider string `json:"provider"`
	API      string `json:"api"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
}

// Store reads and atomically replaces named session snapshots.
type Store struct {
	dir string
}

// New creates a session Store rooted at dir.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("session directory is required")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve session directory: %w", err)
	}
	return &Store{dir: absolute}, nil
}

// Load reads a named session. A missing session returns an empty snapshot.
func (s *Store) Load(name string) (Snapshot, error) {
	path, err := s.path(name)
	if err != nil {
		return Snapshot{}, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("open session %q: %w", name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspect session %q: %w", name, err)
	}
	if info.Size() > maxFileSize {
		return Snapshot{}, fmt.Errorf("session %q exceeds 64 MiB", name)
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxFileSize))
	decoder.DisallowUnknownFields()
	var stored sessionFile
	if err := decoder.Decode(&stored); err != nil {
		return Snapshot{}, fmt.Errorf("decode session %q: %w", name, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Snapshot{}, fmt.Errorf("decode session %q: multiple JSON values are not allowed", name)
		}
		return Snapshot{}, fmt.Errorf("decode session %q: %w", name, err)
	}
	if stored.Version != fileVersion {
		return Snapshot{}, fmt.Errorf("session %q uses %w %d", name, ErrUnsupportedVersion, stored.Version)
	}
	if stored.Revision == 0 {
		return Snapshot{}, fmt.Errorf("session %q has invalid revision 0", name)
	}
	if err := validateBackend(stored.Backend); err != nil {
		return Snapshot{}, fmt.Errorf("decode session %q backend: %w", name, err)
	}

	messages := make([]dora.Message, len(stored.Messages))
	for index, record := range stored.Messages {
		message, err := record.message()
		if err != nil {
			return Snapshot{}, fmt.Errorf("decode session %q message %d: %w", name, index, err)
		}
		messages[index] = message
	}
	return Snapshot{
		Revision:     stored.Revision,
		Backend:      stored.Backend,
		Messages:     messages,
		Continuation: stored.Continuation,
	}, nil
}

// Revision reads only the concurrency revision, regardless of session format.
// It is used by explicit replacement flows that do not consume old contents.
func (s *Store) Revision(name string) (uint64, error) {
	path, err := s.path(name)
	if err != nil {
		return 0, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open session %q: %w", name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("inspect session %q: %w", name, err)
	}
	if info.Size() > maxFileSize {
		return 0, fmt.Errorf("session %q exceeds 64 MiB", name)
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxFileSize))
	var header struct {
		Revision uint64 `json:"revision"`
	}
	if err := decoder.Decode(&header); err != nil {
		return 0, fmt.Errorf("decode session %q revision: %w", name, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return 0, fmt.Errorf("decode session %q: multiple JSON values are not allowed", name)
		}
		return 0, fmt.Errorf("decode session %q revision: %w", name, err)
	}
	if header.Revision == 0 {
		return 0, fmt.Errorf("session %q has invalid revision 0", name)
	}
	return header.Revision, nil
}

// Save commits a snapshot when the stored revision still matches expected.
// The Revision field in next is ignored and assigned by the store.
func (s *Store) Save(name string, expected uint64, next Snapshot) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	currentRevision, err := s.Revision(name)
	if err != nil {
		return err
	}
	if currentRevision != expected {
		return fmt.Errorf("%w for %q: expected %d, found %d", ErrConflict, name, expected, currentRevision)
	}
	if err := validateBackend(next.Backend); err != nil {
		return fmt.Errorf("encode session %q backend: %w", name, err)
	}

	stored := sessionFile{
		Version:      fileVersion,
		Revision:     expected + 1,
		Backend:      next.Backend,
		Continuation: next.Continuation,
		Messages:     make([]messageRecord, len(next.Messages)),
	}
	for index, message := range next.Messages {
		record, err := newMessageRecord(message)
		if err != nil {
			return fmt.Errorf("encode session %q message %d: %w", name, index, err)
		}
		stored.Messages[index] = record
	}
	payload, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session %q: %w", name, err)
	}
	payload = append(payload, '\n')

	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	temporary, err := os.CreateTemp(s.dir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary session: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary session: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary session: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary session: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary session: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit session %q: %w", name, err)
	}
	return nil
}

func (s *Store) path(name string) (string, error) {
	if s == nil || s.dir == "" {
		return "", errors.New("session store is not initialized")
	}
	if !namePattern.MatchString(name) {
		return "", errors.New("session name must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}")
	}
	return filepath.Join(s.dir, name+".json"), nil
}

type sessionFile struct {
	Version      int             `json:"version"`
	Revision     uint64          `json:"revision"`
	Backend      Backend         `json:"backend"`
	Continuation string          `json:"continuation,omitempty"`
	Messages     []messageRecord `json:"messages"`
}

func validateBackend(backend Backend) error {
	if backend.Provider == "" {
		return errors.New("provider is required")
	}
	if backend.API == "" {
		return errors.New("API is required")
	}
	if backend.Model == "" {
		return errors.New("model is required")
	}
	if backend.BaseURL == "" {
		return errors.New("base URL is required")
	}
	return nil
}

type messageRecord struct {
	Role       dora.Role        `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []toolCallRecord `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type toolCallRecord struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func newMessageRecord(message dora.Message) (messageRecord, error) {
	if !validRole(message.Role) {
		return messageRecord{}, fmt.Errorf("unsupported role %q", message.Role)
	}
	record := messageRecord{
		Role:       message.Role,
		Content:    message.Content,
		ToolCallID: message.ToolCallID,
	}
	if message.ToolCalls != nil {
		record.ToolCalls = make([]toolCallRecord, len(message.ToolCalls))
	}
	for index, call := range message.ToolCalls {
		if len(call.Input) > 0 && !json.Valid(call.Input) {
			return messageRecord{}, fmt.Errorf("tool call %d has invalid JSON input", index)
		}
		record.ToolCalls[index] = toolCallRecord{
			ID:    call.ID,
			Name:  call.Name,
			Input: append(json.RawMessage(nil), call.Input...),
		}
	}
	return record, nil
}

func (record messageRecord) message() (dora.Message, error) {
	if !validRole(record.Role) {
		return dora.Message{}, fmt.Errorf("unsupported role %q", record.Role)
	}
	message := dora.Message{
		Role:       record.Role,
		Content:    record.Content,
		ToolCallID: record.ToolCallID,
	}
	if record.ToolCalls != nil {
		message.ToolCalls = make([]dora.ToolCall, len(record.ToolCalls))
	}
	for index, call := range record.ToolCalls {
		if len(call.Input) > 0 && !json.Valid(call.Input) {
			return dora.Message{}, fmt.Errorf("tool call %d has invalid JSON input", index)
		}
		message.ToolCalls[index] = dora.ToolCall{
			ID:    call.ID,
			Name:  call.Name,
			Input: append(json.RawMessage(nil), call.Input...),
		}
	}
	return message, nil
}

func validRole(role dora.Role) bool {
	switch role {
	case dora.RoleSystem, dora.RoleUser, dora.RoleAssistant, dora.RoleTool:
		return true
	default:
		return false
	}
}
