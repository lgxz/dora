//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lgxz/dora/session"
	sqlitesession "github.com/lgxz/dora/session/sqlite"
)

// Re-execute the test binary to exercise main's actual signal registration and
// exit path without installing signal handlers in the parent test process.
func TestSignalHelperProcess(t *testing.T) {
	if os.Getenv("DORA_TEST_SIGNAL_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"dora"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing helper arguments")
}

func TestShutdownSignalPersistsSession(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGTERM, os.Interrupt} {
		t.Run(sig.String(), func(t *testing.T) {
			waiting := make(chan struct{})
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if requests.Add(1) == 1 {
					w.Header().Set("Content-Type", "text/event-stream")
					// An unknown tool produces a normal tool error round, avoiding
					// dependence on a shell or external command in this test.
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"missing_test_tool\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				// The second request proves the first round is complete and
				// signal handling is installed; no timing-based sleep is needed.
				if requests.Load() == 2 {
					close(waiting)
				}
				<-r.Context().Done()
			}))
			defer server.Close()

			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			databasePath := filepath.Join(dir, "session.sqlite")
			config := fmt.Sprintf(`providers:
  - name: signaltest
    base_url: %q
    profiles:
      - name: model
        capabilities: [text]
env:
  SIGNALTEST_API_KEY: test-key
`, server.URL+"/v1")
			if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSignalHelperProcess$", "--",
				"--config", configPath, "--model", "signaltest/model", "--no-skills", "--quiet",
				"--session", databasePath, "save before shutdown")
			cmd.Env = append(os.Environ(), "DORA_TEST_SIGNAL_HELPER=1")
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			waited := false
			defer func() {
				cancel()
				if !waited {
					<-done
				}
			}()
			select {
			case <-waiting:
			case err := <-done:
				waited = true
				t.Fatalf("process exited before signal: %v\n%s", err, output.String())
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err = <-done
			waited = true
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(output.String(), "context canceled") {
				t.Fatalf("expected graceful canceled exit, got %v\n%s", err, output.String())
			}
			store, err := sqlitesession.Open(context.Background(), databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != 1 || page.Turns[0].Status != session.TurnStatusCanceled || page.Turns[0].RoundCount != 1 || page.Turns[0].User != "save before shutdown" {
				t.Fatalf("saved turns = %+v", page)
			}
			rounds, err := store.GetRounds(context.Background(), page.Turns[0].ID, session.RoundOptions{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if len(rounds.Rounds) != 1 || len(rounds.Rounds[0].Tools) != 1 || !strings.Contains(rounds.Rounds[0].Tools[0].Content, "missing_test_tool") {
				t.Fatalf("saved rounds = %+v", rounds)
			}
		})
	}
}
