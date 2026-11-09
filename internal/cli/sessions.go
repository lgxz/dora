package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lgxz/dora/internal/paths"
	jsonlsession "github.com/lgxz/dora/session/jsonl"
)

const (
	sessionExtension = ".jsonl"
	// listSessionsLimit is how many sessions --list-sessions shows.
	listSessionsLimit = 20
	// listPromptRunes bounds the prompt excerpt shown for each session.
	listPromptRunes = 60
	// ambiguousSessionLimit bounds the candidates named in an ambiguity error.
	ambiguousSessionLimit = 5
)

// savedSession is a session file in the sessions directory.
type savedSession struct {
	// ID is the file name without its extension; it starts with the creation
	// time, so IDs sort chronologically and a short prefix usually identifies one.
	ID string
	jsonlsession.Info
}

// sessionSelection says which session a run uses.
type sessionSelection struct {
	// Path is the session file. It is empty for an in-memory session.
	Path string
	// Created reports that Path is a new file Dora chose itself. Such a file is
	// removed again when the run leaves it without any turn.
	Created bool
	// Notice is something the user should hear about, shown unless --quiet.
	Notice string
}

// selectSession decides which session the run uses:
//
//   - --no-session: an in-memory session;
//   - --session PATH: that file, created when missing;
//   - --session ID: the saved session whose ID starts with ID;
//   - --continue: the most recently used saved session of this directory;
//   - otherwise: a new file in the sessions directory.
//
// workdir is the directory the run works in. now names new files.
func selectSession(opts options, workdir string, now time.Time) (sessionSelection, error) {
	switch {
	case opts.noSession && (opts.sessionPath != "" || opts.continueSession):
		return sessionSelection{}, errors.New("--no-session cannot be combined with --session or --continue")
	case opts.sessionPath != "" && opts.continueSession:
		return sessionSelection{}, errors.New("--session and --continue cannot be combined")
	case opts.noSession:
		return sessionSelection{}, nil
	}
	dir, dirErr := paths.SessionsDir()

	if opts.sessionPath != "" {
		path, err := resolveSessionArgument(opts.sessionPath, dir, dirErr)
		return sessionSelection{Path: path}, err
	}
	if dirErr != nil {
		return sessionSelection{Notice: fmt.Sprintf("sessions are not saved: %v", dirErr)}, nil
	}

	var notice string
	if opts.continueSession {
		saved, err := listSavedSessions(dir)
		if err != nil {
			return sessionSelection{}, err
		}
		if latest, ok := latestFor(saved, workdir); ok {
			return sessionSelection{Path: latest.Path}, nil
		}
		notice = fmt.Sprintf("no saved session for %s; starting a new one", workdir)
	}
	path, err := newSessionPath(dir, now, rand.Reader)
	if err != nil {
		return sessionSelection{Notice: fmt.Sprintf("sessions are not saved: %v", err)}, nil
	}
	return sessionSelection{Path: path, Created: true, Notice: notice}, nil
}

// resolveSessionArgument maps the --session argument to a file. An argument
// that names a file keeps its meaning (a path, created when missing). Any
// other bare name is a saved-session ID or a unique prefix of one.
func resolveSessionArgument(arg, dir string, dirErr error) (string, error) {
	if strings.ContainsAny(arg, `/\`) {
		return arg, nil
	}
	if info, err := os.Stat(arg); err == nil && info.Mode().IsRegular() {
		return arg, nil
	}
	if dirErr == nil {
		saved, err := listSavedSessions(dir)
		if err != nil {
			return "", err
		}
		match, err := matchSession(saved, strings.TrimSuffix(arg, sessionExtension))
		if err == nil {
			return match.Path, nil
		}
		if !errors.Is(err, errNoSession) {
			return "", err
		}
	}
	if strings.HasSuffix(arg, sessionExtension) {
		return arg, nil // a new file in the current directory
	}
	return "", fmt.Errorf("no saved session matches %q (list them with --list-sessions, or pass a path such as ./%s%s)", arg, arg, sessionExtension)
}

var errNoSession = errors.New("no saved session matches")

// matchSession finds the session whose ID is prefix, or the only one that
// starts with it.
func matchSession(saved []savedSession, prefix string) (savedSession, error) {
	if prefix == "" {
		return savedSession{}, errNoSession
	}
	var matches []savedSession
	for _, candidate := range saved {
		if candidate.ID == prefix {
			return candidate, nil
		}
		if strings.HasPrefix(candidate.ID, prefix) {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 0:
		return savedSession{}, errNoSession
	case 1:
		return matches[0], nil
	}
	names := make([]string, 0, ambiguousSessionLimit)
	for _, match := range matches {
		if len(names) == ambiguousSessionLimit {
			names = append(names, "...")
			break
		}
		names = append(names, match.ID)
	}
	return savedSession{}, fmt.Errorf("session %q is ambiguous: %s", prefix, strings.Join(names, ", "))
}

// latestFor returns the most recently used session created for workdir.
func latestFor(saved []savedSession, workdir string) (savedSession, bool) {
	want := filepath.Clean(workdir)
	for _, candidate := range saved { // newest first
		if candidate.Workdir != "" && filepath.Clean(candidate.Workdir) == want {
			return candidate, true
		}
	}
	return savedSession{}, false
}

// listSavedSessions returns the readable session files in dir, most recently
// used first. A missing directory holds no sessions.
func listSavedSessions(dir string) ([]savedSession, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sessions directory: %w", err)
	}
	var saved []savedSession
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, sessionExtension) {
			continue
		}
		info, err := jsonlsession.ReadInfo(filepath.Join(dir, name))
		if err != nil {
			continue // not a session file, or not readable: not listed
		}
		saved = append(saved, savedSession{ID: strings.TrimSuffix(name, sessionExtension), Info: info})
	}
	sort.Slice(saved, func(i, j int) bool {
		if !saved[i].ModTime.Equal(saved[j].ModTime) {
			return saved[i].ModTime.After(saved[j].ModTime)
		}
		return saved[i].ID > saved[j].ID
	})
	return saved, nil
}

// newSessionPath creates the sessions directory when needed and returns an
// unused file name in it: the local creation time, then random hex digits so
// that two runs in the same second do not collide.
func newSessionPath(dir string, now time.Time, random io.Reader) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	suffix := make([]byte, 3)
	if _, err := io.ReadFull(random, suffix); err != nil {
		return "", err
	}
	name := now.Format("20060102-150405") + "-" + hex.EncodeToString(suffix) + sessionExtension
	return filepath.Join(dir, name), nil
}

// removeIfEmpty deletes a session file Dora created when the run left nothing
// in it, so failed starts do not litter the sessions directory.
func removeIfEmpty(path string) {
	if info, err := jsonlsession.ReadInfo(path); err == nil && info.Empty {
		_ = os.Remove(path)
	}
}

// printSessions writes the most recently used sessions, one per line.
func printSessions(out io.Writer, dir string) error {
	saved, err := listSavedSessions(dir)
	if err != nil {
		return err
	}
	if len(saved) == 0 {
		_, err = fmt.Fprintf(out, "no saved sessions in %s\n", dir)
		return err
	}
	home, _ := os.UserHomeDir()
	for i, entry := range saved {
		if i == listSessionsLimit {
			_, err = fmt.Fprintf(out, "... %d older sessions in %s\n", len(saved)-listSessionsLimit, dir)
			return err
		}
		workdir := entry.Workdir
		if home != "" && (workdir == home || strings.HasPrefix(workdir, home+string(filepath.Separator))) {
			workdir = "~" + strings.TrimPrefix(workdir, home)
		}
		if workdir == "" {
			workdir = "-"
		}
		if _, err := fmt.Fprintf(out, "%s  %s  %s  %s\n", entry.ID, entry.ModTime.Format("2006-01-02 15:04"), workdir, promptExcerpt(entry.Prompt)); err != nil {
			return err
		}
	}
	return nil
}

// promptExcerpt shortens the first line of a prompt for a listing.
func promptExcerpt(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return "-"
	}
	if utf8.RuneCountInString(line) <= listPromptRunes {
		return line
	}
	runes := []rune(line)
	return string(runes[:listPromptRunes]) + "…"
}
