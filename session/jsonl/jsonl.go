// Package jsonl stores Dora turns in one append-only JSON Lines file.
//
// The first line is a header, {"t":"header","v":1}. Every later line is one
// record, tagged by "t":
//
//	turn     a turn starts (id, optional parent, system, user, time)
//	round    one complete assistant/tool round of a turn
//	attempt  one model call, kept for audit
//	end      the turn's terminal status, result and usage
//
// A turn without an end record was interrupted and is reported as running.
// Opening a file replays it into a small index of byte ranges; message bodies
// stay on disk until they are requested. A torn last line (a crash in the
// middle of a write) is discarded on open.
//
// The file is held open exclusively, so one session file belongs to one Dora
// process at a time.
package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/session"
)

const formatVersion = 1

const (
	recHeader  = "header"
	recTurn    = "turn"
	recRound   = "round"
	recAttempt = "attempt"
	recEnd     = "end"
)

var (
	errNotSession = errors.New("not a Dora JSONL session file (SQLite and older JSON sessions are not supported)")
	errInUse      = errors.New("session file is in use by another process")
)

var (
	_ session.Store         = (*Store)(nil)
	_ session.AttemptReader = (*Store)(nil)
)

// span is the byte range of one record line, without its newline.
type span struct{ off, n int64 }

type turnEntry struct {
	id, parent        int64
	system, user      string
	status            session.TurnStatus
	errorText, result string
	usage, totalUsage *dora.Usage
	at                time.Time
	rounds, attempts  []span
}

// headerRec is the first line of every session file.
type headerRec struct {
	T       string `json:"t"`
	V       int    `json:"v"`
	Workdir string `json:"workdir,omitempty"`
}

// Option configures Open.
type Option func(*Store)

// WithWorkdir records the directory a new session file belongs to in its
// header, so that tools can find the sessions of one directory. It has no
// effect on a file that already exists.
func WithWorkdir(directory string) Option {
	return func(s *Store) { s.workdir = directory }
}

// Store is a JSONL-backed session store.
type Store struct {
	workdir string // written to the header of a new file
	mu      sync.Mutex
	file    *os.File // nil for an in-memory store
	mem     []byte   // contents of an in-memory store
	size    int64    // bytes of valid data
	turns   []*turnEntry
	closed  bool
	broken  error
	writeAt func(p []byte, off int64) (int, error)
}

// Open opens or creates the JSONL session file at path and locks it for this
// process.
func Open(ctx context.Context, path string, options ...Option) (*Store, error) {
	if path == "" {
		return nil, errors.New("session path is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve session path: %w", err)
	}
	if info, err := os.Stat(absolute); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("session %q is not a regular file", absolute)
	}
	file, err := openExclusive(absolute)
	if err != nil {
		if errors.Is(err, errInUse) {
			return nil, fmt.Errorf("session %q is in use by another process", absolute)
		}
		return nil, fmt.Errorf("open session: %w", err)
	}
	s := &Store{file: file, writeAt: file.WriteAt}
	for _, option := range options {
		option(s)
	}
	if err := s.load(); err != nil {
		file.Close()
		return nil, fmt.Errorf("load session %q: %w", absolute, err)
	}
	return s, nil
}

// OpenMemory opens an ephemeral session store. Its contents live only until
// Close.
func OpenMemory(ctx context.Context) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &Store{}
	s.writeAt = func(p []byte, off int64) (int, error) {
		s.mem = append(s.mem[:off], p...)
		return len(p), nil
	}
	if err := s.writeHeader(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the session file.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.mem = nil
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}

func (s *Store) usable() error {
	if s.closed {
		return errors.New("session is closed")
	}
	return s.broken
}

func (s *Store) writeHeader() error {
	line, err := json.Marshal(headerRec{T: recHeader, V: formatVersion, Workdir: s.workdir})
	if err != nil {
		return err
	}
	return s.appendLocked(append(line, '\n'), true)
}

// load replays an existing file into the index. A torn last line is cut off.
func (s *Store) load() error {
	info, err := s.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return s.writeHeader()
	}
	reader := bufio.NewReaderSize(s.file, 256<<10)
	var off int64
	for number := 1; ; number++ {
		data, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if len(data) == 0 {
			break
		}
		if data[len(data)-1] != '\n' {
			if number == 1 {
				return errNotSession
			}
			if err := s.file.Truncate(off); err != nil {
				return fmt.Errorf("discard torn last line: %w", err)
			}
			break
		}
		line := data[:len(data)-1]
		if number == 1 {
			if err := checkHeader(line); err != nil {
				return err
			}
		} else if err := s.apply(off, line); err != nil {
			return fmt.Errorf("line %d: %w", number, err)
		}
		off += int64(len(data))
	}
	s.size = off
	return nil
}

func checkHeader(line []byte) error {
	var header struct {
		T string `json:"t"`
		V int    `json:"v"`
	}
	if json.Unmarshal(line, &header) != nil || header.T != recHeader {
		return errNotSession
	}
	if header.V != formatVersion {
		return fmt.Errorf("unsupported session format version %d", header.V)
	}
	return nil
}

// Wire records.

type turnRec struct {
	T       string    `json:"t"`
	ID      int64     `json:"id"`
	Parent  int64     `json:"parent,omitempty"`
	System  string    `json:"system,omitempty"`
	Context string    `json:"context,omitempty"` // preamble shown to the model in front of User
	User    string    `json:"user"`
	At      time.Time `json:"at"`
}

type roundRec struct {
	T         string       `json:"t"`
	Turn      int64        `json:"turn"`
	Index     int          `json:"i"`
	Assistant messageRec   `json:"assistant"`
	Tools     []messageRec `json:"tools,omitempty"`
	Usage     *dora.Usage  `json:"usage,omitempty"`
}

type attemptRec struct {
	T      string            `json:"t"`
	Turn   int64             `json:"turn"`
	Index  int               `json:"i"`
	Record dora.ModelAttempt `json:"record"`
}

type endRec struct {
	T          string             `json:"t"`
	Turn       int64              `json:"turn"`
	Status     session.TurnStatus `json:"status"`
	Error      string             `json:"error,omitempty"`
	Result     string             `json:"result,omitempty"`
	System     string             `json:"system,omitempty"`
	Usage      *dora.Usage        `json:"usage,omitempty"`
	TotalUsage *dora.Usage        `json:"total_usage,omitempty"`
	At         time.Time          `json:"at"`
}

type messageRec struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	Reasoning  string           `json:"reasoning,omitempty"`
	Images     []dora.Image     `json:"images,omitempty"`
	ToolCalls  []toolCallRecord `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type toolCallRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Bytes are base64-encoded by encoding/json, preserving even malformed JSON
	// and invalid UTF-8 without validation or normalization.
	Input []byte `json:"input_bytes"`
}

func encodeMessage(m dora.Message) messageRec {
	rec := messageRec{Role: string(m.Role), Content: m.Content, Reasoning: m.Reasoning, Images: m.Images, ToolCallID: m.ToolCallID}
	for _, call := range m.ToolCalls {
		rec.ToolCalls = append(rec.ToolCalls, toolCallRecord{ID: call.ID, Name: call.Name, Input: append([]byte(nil), call.Input...)})
	}
	return rec
}

func (r messageRec) message() dora.Message {
	m := dora.Message{Role: dora.Role(r.Role), Content: r.Content, Reasoning: r.Reasoning, Images: r.Images, ToolCallID: r.ToolCallID}
	for _, call := range r.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, dora.ToolCall{ID: call.ID, Name: call.Name, Input: append(json.RawMessage(nil), call.Input...)})
	}
	return m
}

// apply indexes one record line located at off. It validates the record
// against the turns seen so far, so replaying a damaged file fails loudly.
func (s *Store) apply(off int64, line []byte) error {
	var peek struct {
		T     string `json:"t"`
		Turn  int64  `json:"turn"`
		Index int    `json:"i"`
	}
	if err := json.Unmarshal(line, &peek); err != nil {
		return fmt.Errorf("decode record: %w", err)
	}
	where := span{off: off, n: int64(len(line))}
	switch peek.T {
	case recTurn:
		var rec turnRec
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("decode turn: %w", err)
		}
		if rec.ID != int64(len(s.turns))+1 {
			return fmt.Errorf("turn ID %d is out of sequence", rec.ID)
		}
		if rec.Parent < 0 || rec.Parent >= rec.ID {
			return fmt.Errorf("turn %d has invalid parent %d", rec.ID, rec.Parent)
		}
		s.turns = append(s.turns, &turnEntry{id: rec.ID, parent: rec.Parent, system: rec.System, user: rec.User, status: session.TurnStatusRunning, at: rec.At})
	case recRound, recAttempt:
		entry, err := s.runningTurn(peek.Turn)
		if err != nil {
			return err
		}
		list := &entry.rounds
		if peek.T == recAttempt {
			list = &entry.attempts
		}
		if peek.Index != len(*list) {
			return fmt.Errorf("turn %d %s index %d is out of sequence", entry.id, peek.T, peek.Index)
		}
		*list = append(*list, where)
	case recEnd:
		var rec endRec
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("decode end: %w", err)
		}
		entry, err := s.runningTurn(rec.Turn)
		if err != nil {
			return err
		}
		if err := validateEnd(rec); err != nil {
			return fmt.Errorf("turn %d: %w", rec.Turn, err)
		}
		if rec.System != "" {
			entry.system = rec.System
		}
		entry.status, entry.errorText, entry.result = rec.Status, rec.Error, rec.Result
		entry.usage, entry.totalUsage, entry.at = rec.Usage, rec.TotalUsage, rec.At
	default:
		return fmt.Errorf("unknown record type %q", peek.T)
	}
	return nil
}

func (s *Store) runningTurn(id int64) (*turnEntry, error) {
	if id <= 0 || id > int64(len(s.turns)) {
		return nil, fmt.Errorf("record refers to unknown turn %d", id)
	}
	entry := s.turns[id-1]
	if entry.status != session.TurnStatusRunning {
		return nil, fmt.Errorf("record refers to finished turn %d", id)
	}
	return entry, nil
}

func validateEnd(rec endRec) error {
	switch rec.Status {
	case session.TurnStatusCompleted:
		if rec.Error != "" {
			return errors.New("completed turn has an error")
		}
	case session.TurnStatusMaxRounds, session.TurnStatusFailed, session.TurnStatusCanceled:
		if rec.Error == "" {
			return fmt.Errorf("%s turn has no error", rec.Status)
		}
		if rec.Result != "" || rec.Usage != nil {
			return fmt.Errorf("%s turn has a result", rec.Status)
		}
	default:
		return fmt.Errorf("invalid terminal status %q", rec.Status)
	}
	return nil
}

// lines accumulates encoded records for one atomic append.
type lines struct {
	buf  bytes.Buffer
	ends []int
	err  error
}

func (l *lines) add(record any) {
	if l.err != nil {
		return
	}
	encoder := json.NewEncoder(&l.buf)
	encoder.SetEscapeHTML(false)
	l.err = encoder.Encode(record)
	l.ends = append(l.ends, l.buf.Len())
}

// appendLocked writes data at the end of the file. On failure it cuts the file
// back to its previous size so a retry cannot leave a partial or duplicate
// record behind.
func (s *Store) appendLocked(data []byte, sync bool) error {
	n, err := s.writeAt(data, s.size)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil && sync && s.file != nil {
		err = s.file.Sync()
	}
	if err != nil {
		var cut error
		if s.file != nil {
			cut = s.file.Truncate(s.size)
		} else {
			s.mem = s.mem[:s.size]
		}
		if cut != nil {
			s.broken = fmt.Errorf("session file is damaged and cannot be repaired: %w", cut)
		}
		return fmt.Errorf("write session: %w", err)
	}
	s.size += int64(len(data))
	return nil
}

// commitLocked appends a batch of records and indexes them.
func (s *Store) commitLocked(batch *lines, sync bool) error {
	data := batch.buf.Bytes()
	base := s.size
	if err := s.appendLocked(data, sync); err != nil {
		return err
	}
	start := 0
	for _, end := range batch.ends {
		if err := s.apply(base+int64(start), data[start:end-1]); err != nil {
			s.broken = fmt.Errorf("session index is inconsistent: %w", err)
			return s.broken
		}
		start = end
	}
	return nil
}

// CommitTurn saves a completed turn and all of its rounds in one append.
// Production code saves turns incrementally through BeginTurn; the Commit
// methods serve callers that only have a finished Turn.
func (s *Store) CommitTurn(ctx context.Context, turn *dora.Turn) (int64, error) {
	return s.commit(ctx, 0, turn, session.TurnStatusCompleted, nil)
}

// CommitMaxRounds appends an incomplete turn stopped by the round limit.
func (s *Store) CommitMaxRounds(ctx context.Context, turn *dora.Turn, cause error) (int64, error) {
	return s.commit(ctx, 0, turn, session.TurnStatusMaxRounds, cause)
}

// CommitFailed appends an incomplete turn stopped by an error. Only complete
// assistant/tool rounds are stored; partial streamed model output is not.
func (s *Store) CommitFailed(ctx context.Context, turn *dora.Turn, cause error) (int64, error) {
	return s.commit(ctx, 0, turn, session.TurnStatusFailed, cause)
}

// CommitCanceled appends an incomplete turn stopped by context cancellation.
func (s *Store) CommitCanceled(ctx context.Context, turn *dora.Turn, cause error) (int64, error) {
	return s.commit(ctx, 0, turn, session.TurnStatusCanceled, cause)
}

// CommitChild records a terminal child without changing the parent transcript or usage.
func (s *Store) CommitChild(ctx context.Context, parentID int64, turn *dora.Turn, cause error) (int64, error) {
	if parentID <= 0 || turn == nil {
		return 0, errors.New("child requires a parent ID and turn")
	}
	status := session.TurnStatusCompleted
	if !turn.Completed() {
		if cause == nil {
			return 0, errors.New("incomplete child requires an error")
		}
		switch {
		case errors.Is(cause, dora.ErrMaxRounds):
			status = session.TurnStatusMaxRounds
		case errors.Is(cause, context.Canceled):
			status = session.TurnStatusCanceled
		default:
			status = session.TurnStatusFailed
		}
	}
	return s.commit(ctx, parentID, turn, status, cause)
}

func (s *Store) commit(ctx context.Context, parentID int64, turn *dora.Turn, status session.TurnStatus, cause error) (int64, error) {
	return (&turnWriter{store: s, parentID: parentID, turn: turn}).Finish(ctx, status, cause)
}

// BeginTurn implements session.Store. It does no storage work: the turn is
// created by the first Sync that has a round to save, or by Finish.
func (s *Store) BeginTurn(turn *dora.Turn) session.TurnWriter {
	return &turnWriter{store: s, turn: turn}
}

// turnWriter saves one Turn incrementally. It remembers how many rounds and
// model attempts are already stored and appends only the rest, advancing those
// counts only after the append succeeds, so a failed save leaves it ready to
// retry. It is not safe for concurrent use.
type turnWriter struct {
	store    *Store
	parentID int64
	turn     *dora.Turn
	id       int64
	rounds   int
	attempts int
}

func (w *turnWriter) ID() int64 { return w.id }

// Sync implements session.TurnWriter.
func (w *turnWriter) Sync(ctx context.Context) error {
	if w.turn == nil {
		return errors.New("cannot save a nil turn")
	}
	_, err := w.save(ctx, session.TurnStatusRunning, "", "", nil)
	return err
}

// Finish implements session.TurnWriter.
func (w *turnWriter) Finish(ctx context.Context, status session.TurnStatus, cause error) (int64, error) {
	result, errorText, usage, err := terminalFields(w.turn, status, cause)
	if err != nil {
		return 0, err
	}
	return w.save(ctx, status, result, errorText, usage)
}

// terminalFields validates that turn and cause agree with a terminal status and
// returns the values stored for it.
func terminalFields(turn *dora.Turn, status session.TurnStatus, cause error) (result, errorText string, usage *dora.Usage, err error) {
	if turn == nil {
		return "", "", nil, errors.New("cannot save a nil turn")
	}
	switch status {
	case session.TurnStatusCompleted:
		if !turn.Completed() {
			return "", "", nil, errors.New("cannot commit an incomplete turn")
		}
		result, _ = turn.Result()
		return result, "", turn.Usage(), nil
	case session.TurnStatusMaxRounds:
		if turn.Completed() {
			return "", "", nil, errors.New("cannot commit a completed turn as max rounds")
		}
		if !errors.Is(cause, dora.ErrMaxRounds) {
			return "", "", nil, errors.New("max-round turn requires ErrMaxRounds")
		}
		return "", cause.Error(), nil, nil
	case session.TurnStatusFailed:
		if turn.Completed() {
			return "", "", nil, errors.New("cannot commit a completed turn as failed")
		}
		if cause == nil {
			return "", "", nil, errors.New("failed turn requires an error")
		}
		return "", cause.Error(), nil, nil
	case session.TurnStatusCanceled:
		if turn.Completed() {
			return "", "", nil, errors.New("cannot commit a completed turn as canceled")
		}
		if !errors.Is(cause, context.Canceled) {
			return "", "", nil, errors.New("canceled turn requires context.Canceled")
		}
		return "", cause.Error(), nil, nil
	default:
		return "", "", nil, fmt.Errorf("invalid terminal turn status %q", status)
	}
}

// save appends the turn record (first use), the rounds and attempts not yet
// stored, and for terminal statuses the end record, as one write.
func (w *turnWriter) save(ctx context.Context, status session.TurnStatus, result, errorText string, usage *dora.Usage) (int64, error) {
	s := w.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return 0, err
	}
	rounds, attempts := w.turn.Rounds(), w.turn.Attempts()
	terminal := status != session.TurnStatusRunning
	if !terminal {
		// Nothing new to save, and no reason to create a turn that has not
		// completed a round.
		if w.id == 0 && len(rounds) == 0 {
			return 0, nil
		}
		if w.id != 0 && len(rounds) == w.rounds && len(attempts) == w.attempts {
			return w.id, nil
		}
	}
	id, system := w.id, ""
	if id != 0 {
		entry := s.turns[id-1]
		if entry.status != session.TurnStatusRunning {
			return 0, fmt.Errorf("turn %d is no longer running", id)
		}
		system = entry.system
	} else if w.parentID > int64(len(s.turns)) {
		return 0, fmt.Errorf("%w: parent %d", session.ErrNotFound, w.parentID)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	var batch lines
	if id == 0 {
		id = int64(len(s.turns)) + 1
		system = w.turn.System()
		batch.add(turnRec{T: recTurn, ID: id, Parent: w.parentID, System: system, Context: w.turn.Context(), User: w.turn.User(), At: now})
	}
	for i := w.attempts; i < len(attempts); i++ {
		batch.add(attemptRec{T: recAttempt, Turn: id, Index: i, Record: attempts[i]})
	}
	for i := w.rounds; i < len(rounds); i++ {
		rec := roundRec{T: recRound, Turn: id, Index: i, Assistant: encodeMessage(rounds[i].Assistant), Usage: rounds[i].Usage}
		for _, message := range rounds[i].Tools {
			rec.Tools = append(rec.Tools, encodeMessage(message))
		}
		batch.add(rec)
	}
	if terminal {
		end := endRec{T: recEnd, Turn: id, Status: status, Error: errorText, Result: result, Usage: usage, TotalUsage: w.turn.TotalUsage(), At: now}
		if latest := w.turn.System(); latest != system {
			end.System = latest
		}
		batch.add(end)
	}
	if batch.err != nil {
		return 0, fmt.Errorf("encode turn %d: %w", id, batch.err)
	}
	if err := s.commitLocked(&batch, terminal); err != nil {
		return 0, err
	}
	w.id, w.rounds, w.attempts = id, len(rounds), len(attempts)
	return id, nil
}

func (s *Store) read(sp span) ([]byte, error) {
	if s.file == nil {
		return s.mem[sp.off : sp.off+sp.n], nil
	}
	data := make([]byte, sp.n)
	if n, err := s.file.ReadAt(data, sp.off); n != len(data) {
		return nil, fmt.Errorf("read session: %w", err)
	}
	return data, nil
}

// ListTurns returns saved turns newest first.
func (s *Store) ListTurns(ctx context.Context, options session.ListOptions) (session.TurnPage, error) {
	if options.Offset < 0 || options.Limit <= 0 {
		return session.TurnPage{}, errors.New("turn list offset must be non-negative and limit must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return session.TurnPage{}, err
	}
	page := session.TurnPage{Total: len(s.turns), Offset: options.Offset, Limit: options.Limit, Turns: []session.TurnSummary{}}
	for i := len(s.turns) - 1 - options.Offset; i >= 0 && len(page.Turns) < options.Limit; i-- {
		entry := s.turns[i]
		summary := session.TurnSummary{
			ID: entry.id, User: entry.user, Result: entry.result, Status: entry.status, Error: entry.errorText,
			RoundCount: len(entry.rounds), Usage: entry.usage, TotalUsage: entry.totalUsage, CommittedAt: entry.at,
		}
		if entry.parent != 0 {
			parent := entry.parent
			summary.ParentTurnID = &parent
		}
		page.Turns = append(page.Turns, summary)
	}
	return page, nil
}

func (s *Store) lookup(id int64) (*turnEntry, error) {
	if id <= 0 {
		return nil, errors.New("turn ID must be positive")
	}
	if id > int64(len(s.turns)) {
		return nil, fmt.Errorf("%w: %d", session.ErrNotFound, id)
	}
	return s.turns[id-1], nil
}

// GetRounds returns a chronological page of complete rounds from one turn.
func (s *Store) GetRounds(ctx context.Context, id int64, options session.RoundOptions) (session.RoundPage, error) {
	if options.Offset < 0 || options.Limit <= 0 {
		return session.RoundPage{}, errors.New("round offset must be non-negative and limit must be positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return session.RoundPage{}, err
	}
	entry, err := s.lookup(id)
	if err != nil {
		return session.RoundPage{}, err
	}
	page := session.RoundPage{Total: len(entry.rounds), Offset: options.Offset, Limit: options.Limit, Rounds: []dora.Round{}}
	end := min(options.Offset+options.Limit, len(entry.rounds))
	validator := dora.NewTurn("")
	for index := options.Offset; index < end; index++ {
		data, err := s.read(entry.rounds[index])
		if err != nil {
			return session.RoundPage{}, fmt.Errorf("get turn %d round %d: %w", id, index, err)
		}
		var rec roundRec
		if err := json.Unmarshal(data, &rec); err != nil {
			return session.RoundPage{}, fmt.Errorf("decode turn %d round %d: %w", id, index, err)
		}
		round := dora.Round{Assistant: rec.Assistant.message(), Usage: rec.Usage}
		for _, message := range rec.Tools {
			round.Tools = append(round.Tools, message.message())
		}
		if err := validator.AppendRound(round, ""); err != nil {
			return session.RoundPage{}, fmt.Errorf("decode turn %d round %d: %w", id, index, err)
		}
		page.Rounds = append(page.Rounds, round)
	}
	return page, nil
}

// GetAttempts returns audit records independently of model-visible tool rounds.
func (s *Store) GetAttempts(ctx context.Context, id int64, options session.RoundOptions) (session.AttemptPage, error) {
	if options.Offset < 0 || options.Limit <= 0 {
		return session.AttemptPage{}, errors.New("attempt offset must be non-negative and limit positive")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return session.AttemptPage{}, err
	}
	entry, err := s.lookup(id)
	if err != nil {
		return session.AttemptPage{}, err
	}
	page := session.AttemptPage{Total: len(entry.attempts), Offset: options.Offset, Limit: options.Limit, Attempts: []dora.ModelAttempt{}}
	end := min(options.Offset+options.Limit, len(entry.attempts))
	for index := options.Offset; index < end; index++ {
		data, err := s.read(entry.attempts[index])
		if err != nil {
			return session.AttemptPage{}, fmt.Errorf("get turn %d attempt %d: %w", id, index, err)
		}
		var rec attemptRec
		if err := json.Unmarshal(data, &rec); err != nil {
			return session.AttemptPage{}, fmt.Errorf("decode turn %d attempt %d: %w", id, index, err)
		}
		page.Attempts = append(page.Attempts, rec.Record)
	}
	return page, nil
}
