package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora/session"
	jsonlsession "github.com/lgxz/dora/session/jsonl"
)

// writeSavedSession writes a minimal session file into dir and gives it a
// modification time, which decides the order sessions are listed in.
func writeSavedSession(t *testing.T, dir, id, workdir, prompt string, modTime time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(map[string]any{"t": "header", "v": 1, "workdir": workdir})
	if err != nil {
		t.Fatal(err)
	}
	content := string(header) + "\n"
	if prompt != "" {
		turn, err := json.Marshal(map[string]any{"t": "turn", "id": 1, "user": prompt, "at": "2026-01-01T00:00:00Z"})
		if err != nil {
			t.Fatal(err)
		}
		content += string(turn) + "\n"
	}
	path := filepath.Join(dir, id+sessionExtension)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNewSessionPathNamesTheFileByTimeAndRandomSuffix(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	now := time.Date(2026, 11, 8, 20, 58, 3, 0, time.Local)
	path, err := newSessionPath(dir, now, bytes.NewReader([]byte{0xab, 0xcd, 0xef}))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "20261108-205803-abcdef.jsonl"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("sessions directory was not created: %v", err)
	}
	if _, err := newSessionPath(dir, now, bytes.NewReader(nil)); err == nil {
		t.Fatal("expected an error when no random bytes are available")
	}
}

func TestMatchSession(t *testing.T) {
	saved := []savedSession{{ID: "20261108-205803-abcdef"}, {ID: "20261108-205900-123456"}, {ID: "20261109-080000-fedcba"}}
	for _, tc := range []struct {
		prefix, want string
	}{
		{"20261108-205803-abcdef", "20261108-205803-abcdef"}, // exact
		{"20261109", "20261109-080000-fedcba"},               // unique prefix
		{"20261108-2059", "20261108-205900-123456"},
	} {
		match, err := matchSession(saved, tc.prefix)
		if err != nil || match.ID != tc.want {
			t.Errorf("matchSession(%q) = %q, %v; want %q", tc.prefix, match.ID, err, tc.want)
		}
	}
	if _, err := matchSession(saved, "20261108"); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "20261108-205803-abcdef") {
		t.Errorf("ambiguous prefix error = %v", err)
	}
	for _, prefix := range []string{"", "2027"} {
		if _, err := matchSession(saved, prefix); err == nil {
			t.Errorf("matchSession(%q) found a session", prefix)
		}
	}
}

func TestListSavedSessionsOrdersByUseAndSkipsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	writeSavedSession(t, dir, "old", "/a", "old prompt", base)
	writeSavedSession(t, dir, "new", "/b", "new prompt", base.Add(time.Hour))
	if err := os.WriteFile(filepath.Join(dir, "notes.jsonl"), []byte("not a session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	saved, err := listSavedSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 || saved[0].ID != "new" || saved[1].ID != "old" {
		t.Fatalf("saved = %#v", saved)
	}
	if missing, err := listSavedSessions(filepath.Join(dir, "missing")); err != nil || len(missing) != 0 {
		t.Fatalf("missing directory: %v, %v", missing, err)
	}
}

func TestLatestForPicksTheNewestSessionOfTheDirectory(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	writeSavedSession(t, dir, "a-old", "/work/a", "p", base)
	writeSavedSession(t, dir, "b-new", "/work/b", "p", base.Add(2*time.Hour))
	writeSavedSession(t, dir, "a-new", "/work/a", "p", base.Add(time.Hour))
	writeSavedSession(t, dir, "legacy", "", "p", base.Add(3*time.Hour)) // no workdir recorded
	saved, err := listSavedSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := latestFor(saved, "/work/a/"); !ok || got.ID != "a-new" {
		t.Fatalf("latestFor(/work/a) = %q, %v", got.ID, ok)
	}
	if _, ok := latestFor(saved, "/work/c"); ok {
		t.Fatal("found a session for a directory without one")
	}
}

func TestResolveSessionArgument(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	writeSavedSession(t, dir, "20261108-205803-abcdef", "/w", "p", time.Now())
	writeSavedSession(t, dir, "20261109-080000-fedcba", "/w", "p", time.Now())
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.WriteFile("local", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		arg, want string
	}{
		// Paths, an existing file in the directory, an ID prefix, a pasted file
		// name, and a new file all keep their meaning.
		{"./session.jsonl", "./session.jsonl"},
		{"sub/dir/s.jsonl", "sub/dir/s.jsonl"},
		{"local", "local"},
		{"20261109", filepath.Join(dir, "20261109-080000-fedcba.jsonl")},
		{"20261108-205803-abcdef.jsonl", filepath.Join(dir, "20261108-205803-abcdef.jsonl")},
		{"fresh.jsonl", "fresh.jsonl"},
	} {
		got, err := resolveSessionArgument(tc.arg, dir, nil)
		if err != nil || got != tc.want {
			t.Errorf("resolveSessionArgument(%q) = %q, %v; want %q", tc.arg, got, err, tc.want)
		}
	}
	if _, err := resolveSessionArgument("2027", dir, nil); err == nil || !strings.Contains(err.Error(), "--list-sessions") {
		t.Errorf("unknown ID error = %v", err)
	}
	if _, err := resolveSessionArgument("20261", dir, nil); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous ID error = %v", err)
	}
	// Without a sessions directory a bare name can still be a new file, and a
	// bare name that is no file fails with the same hint.
	if got, err := resolveSessionArgument("fresh.jsonl", "", fmt.Errorf("no home")); err != nil || got != "fresh.jsonl" {
		t.Errorf("without sessions dir: %q, %v", got, err)
	}
	if _, err := resolveSessionArgument("unknown", "", fmt.Errorf("no home")); err == nil {
		t.Error("bare unknown name was accepted")
	}
}

func TestSelectSession(t *testing.T) {
	doraHome := t.TempDir()
	t.Setenv("DORA_HOME", doraHome)
	sessions := filepath.Join(doraHome, "sessions")
	now := time.Date(2026, 11, 8, 20, 58, 3, 0, time.Local)

	t.Run("incompatible options", func(t *testing.T) {
		for _, opts := range []options{
			{noSession: true, sessionPath: "x"},
			{noSession: true, continueSession: true},
			{sessionPath: "x", continueSession: true},
		} {
			if _, err := selectSession(opts, "/w", now); err == nil {
				t.Errorf("selectSession(%+v) accepted incompatible options", opts)
			}
		}
	})
	t.Run("no session", func(t *testing.T) {
		got, err := selectSession(options{noSession: true}, "/w", now)
		if err != nil || got != (sessionSelection{}) {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("default is a new saved session", func(t *testing.T) {
		got, err := selectSession(options{}, "/w", now)
		if err != nil || !got.Created || filepath.Dir(got.Path) != sessions || got.Notice != "" {
			t.Fatalf("got %+v, %v", got, err)
		}
		if !strings.HasPrefix(filepath.Base(got.Path), "20261108-205803-") {
			t.Fatalf("path = %q", got.Path)
		}
	})
	t.Run("explicit path is used as is", func(t *testing.T) {
		got, err := selectSession(options{sessionPath: "./mine.jsonl"}, "/w", now)
		if err != nil || got.Path != "./mine.jsonl" || got.Created {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("continue picks the newest session of the directory", func(t *testing.T) {
		base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
		writeSavedSession(t, sessions, "x-old", "/w", "p", base)
		want := writeSavedSession(t, sessions, "x-new", "/w", "p", base.Add(time.Hour))
		writeSavedSession(t, sessions, "x-other", "/other", "p", base.Add(2*time.Hour))
		got, err := selectSession(options{continueSession: true}, "/w", now)
		if err != nil || got.Path != want || got.Created || got.Notice != "" {
			t.Fatalf("got %+v, %v; want %q", got, err, want)
		}
	})
	t.Run("continue without a session starts a new one", func(t *testing.T) {
		got, err := selectSession(options{continueSession: true}, "/nowhere", now)
		if err != nil || !got.Created || !strings.Contains(got.Notice, "/nowhere") {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
	t.Run("unwritable sessions directory falls back to memory", func(t *testing.T) {
		blocked := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocked, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DORA_HOME", blocked)
		got, err := selectSession(options{}, "/w", now)
		if err != nil || got.Path != "" || got.Created || !strings.Contains(got.Notice, "not saved") {
			t.Fatalf("got %+v, %v", got, err)
		}
	})
}

func TestRemoveIfEmptyOnlyRemovesSessionsWithoutTurns(t *testing.T) {
	dir := t.TempDir()
	empty := writeSavedSession(t, dir, "empty", "/w", "", time.Now())
	used := writeSavedSession(t, dir, "used", "/w", "prompt", time.Now())
	other := filepath.Join(dir, "other.jsonl")
	if err := os.WriteFile(other, []byte("not a session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{empty, used, other, filepath.Join(dir, "missing.jsonl")} {
		removeIfEmpty(path)
	}
	for path, wantExists := range map[string]bool{empty: false, used: true, other: true} {
		if _, err := os.Stat(path); (err == nil) != wantExists {
			t.Errorf("%s exists = %v, want %v", filepath.Base(path), err == nil, wantExists)
		}
	}
}

func TestPrintSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(t.TempDir(), "sessions")
	var out bytes.Buffer

	if err := printSessions(&out, dir); err != nil || !strings.Contains(out.String(), "no saved sessions in "+dir) {
		t.Fatalf("empty listing = %q, %v", out.String(), err)
	}

	base := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	writeSavedSession(t, dir, "20261108-120000-aaaaaa", filepath.Join(home, "proj"), "check the disk\nsecond line", base)
	writeSavedSession(t, dir, "20261108-130000-bbbbbb", "", strings.Repeat("长", 80), base.Add(time.Hour))
	out.Reset()
	if err := printSessions(&out, dir); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	wantNewest := "20261108-130000-bbbbbb  2026-11-08 13:00  -  " + strings.Repeat("长", listPromptRunes) + "…"
	wantOlder := "20261108-120000-aaaaaa  2026-11-08 12:00  ~" + string(filepath.Separator) + "proj  check the disk"
	if len(lines) != 2 || lines[0] != wantNewest || lines[1] != wantOlder {
		t.Fatalf("listing = %q", lines)
	}

	// More than the limit: the newest are shown, the rest is counted.
	for i := 0; i < listSessionsLimit; i++ {
		writeSavedSession(t, dir, fmt.Sprintf("extra-%02d", i), "/w", "p", base.Add(time.Duration(i+2)*time.Hour))
	}
	out.Reset()
	if err := printSessions(&out, dir); err != nil {
		t.Fatal(err)
	}
	lines = strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != listSessionsLimit+1 || !strings.HasPrefix(lines[len(lines)-1], "... 2 older sessions in ") {
		t.Fatalf("limited listing has %d lines, last = %q", len(lines), lines[len(lines)-1])
	}
}

func TestPromptExcerpt(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "-",
		"  \n ":                  "-",
		"  spaced   out\ttext  ": "spaced out text",
		"first\nsecond":          "first",
	} {
		if got := promptExcerpt(in); got != want {
			t.Errorf("promptExcerpt(%q) = %q, want %q", in, got, want)
		}
	}
}

// sessionTestEnv prepares a configuration and a model that answers every
// request, recording the first user message of each request.
type sessionTestEnv struct {
	t          *testing.T
	configPath string
	client     *http.Client
	userInputs []string
}

func newSessionTestEnv(t *testing.T) *sessionTestEnv {
	t.Helper()
	env := &sessionTestEnv{t: t}
	env.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		env.userInputs = append(env.userInputs, body["messages"].([]any)[1].(map[string]any)["content"].(string))
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"answer"}}]}`), nil
	})}
	env.configPath = filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, env.configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`); err != nil {
		t.Fatal(err)
	}
	return env
}

func (env *sessionTestEnv) run(args ...string) string {
	env.t.Helper()
	var stdout bytes.Buffer
	full := append([]string{"--quiet", "--no-skills", "--config", env.configPath}, args...)
	if err := Run(context.Background(), full, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: io.Discard,
		StdinIsTerminal: true, HTTPClient: env.client,
	}); err != nil {
		env.t.Fatalf("Run(%v): %v", args, err)
	}
	return stdout.String()
}

func savedSessionFiles(t *testing.T) []string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join(os.Getenv("DORA_HOME"), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*"+sessionExtension))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func turnPrompts(t *testing.T, path string) []string {
	t.Helper()
	store, err := jsonlsession.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var prompts []string
	for i := len(page.Turns) - 1; i >= 0; i-- { // oldest first
		prompts = append(prompts, page.Turns[i].User)
	}
	return prompts
}

func TestRunSavesSessionsAndContinuesThem(t *testing.T) {
	t.Setenv("DORA_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	env := newSessionTestEnv(t)
	hint := "use the history tool to read them"

	// A plain run is saved without any flag.
	env.run("first")
	files := savedSessionFiles(t)
	if len(files) != 1 {
		t.Fatalf("saved sessions after one run = %v", files)
	}
	if strings.Contains(env.userInputs[0], hint) {
		t.Fatalf("a new session mentions earlier turns: %q", env.userInputs[0])
	}

	// --continue appends to it, and tells the model about the earlier turn.
	env.run("--continue", "second")
	if got := savedSessionFiles(t); len(got) != 1 || got[0] != files[0] {
		t.Fatalf("saved sessions after --continue = %v", got)
	}
	if !strings.Contains(env.userInputs[1], hint) {
		t.Fatalf("a continued session does not mention earlier turns: %q", env.userInputs[1])
	}

	// A bare ID prefix selects it too.
	id := strings.TrimSuffix(filepath.Base(files[0]), sessionExtension)
	env.run("--session", id[:len("20261108-2058")], "third")
	if prompts := turnPrompts(t, files[0]); strings.Join(prompts, ",") != "first,second,third" {
		t.Fatalf("turns = %v", prompts)
	}

	// --no-session leaves nothing behind.
	env.run("--no-session", "ephemeral")
	if got := savedSessionFiles(t); len(got) != 1 {
		t.Fatalf("saved sessions after --no-session = %v", got)
	}

	// Without --continue a run starts its own session.
	env.run("separate")
	if got := savedSessionFiles(t); len(got) != 2 {
		t.Fatalf("saved sessions after a separate run = %v", got)
	}

	// The listing names them, newest use first.
	listing := env.run("--list-sessions")
	if !strings.Contains(listing, id) || !strings.Contains(listing, "first") || !strings.Contains(listing, "separate") {
		t.Fatalf("listing = %q", listing)
	}
}

func TestRunRemovesAnAutomaticSessionThatNeverGotATurn(t *testing.T) {
	t.Setenv("DORA_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, fmt.Sprintf(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
skills:
  directories:
    - %s
`, filepath.Join(root, "missing"))); err != nil {
		t.Fatal(err)
	}
	// The skill directory is only checked after the session was opened.
	err := Run(context.Background(), []string{"--quiet", "--config", configPath, "hello"}, IO{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, StdinIsTerminal: true,
	})
	if err == nil || !strings.Contains(err.Error(), "inspect skill directory") {
		t.Fatalf("error = %v", err)
	}
	if files := savedSessionFiles(t); len(files) != 0 {
		t.Fatalf("a failed start left sessions behind: %v", files)
	}
}

func TestRunListSessionsRejectsAPrompt(t *testing.T) {
	t.Setenv("DORA_HOME", t.TempDir())
	err := Run(context.Background(), []string{"--list-sessions", "hello"}, IO{Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "does not accept a prompt") {
		t.Fatalf("error = %v", err)
	}
}
