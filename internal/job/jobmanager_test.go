package job

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestAdoptAndWait(t *testing.T) {
	m := New()
	cmd := exec.Command("sh", "-c", "echo hello; sleep 0.2; echo world")
	out := &OutputBuffer{}
	cmd.Stdout = out.StdoutWriter()
	cmd.Stderr = out.StderrWriter()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	j := m.AdoptCommand("bash", cmd, func() {}, "echo hello; sleep 0.2; echo world", out, waitDone)
	if j.ID != "bash_0" {
		t.Fatalf("job ID = %q, want bash_0", j.ID)
	}

	// Wait for completion
	done, ok := m.Poll(j.ID, 5*time.Second)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != StatusDone {
		t.Fatalf("expected done, got %s", done.Status)
	}
	if done.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", done.ExitCode)
	}
	if done.Stdout != "hello\nworld\n" {
		t.Fatalf("unexpected stdout: %q", done.Stdout)
	}
	if m.HasActiveJobs() {
		t.Fatalf("expected no active jobs after completion")
	}
}

func TestAdoptRunning(t *testing.T) {
	m := New()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 5")
	out := &OutputBuffer{}
	cmd.Stdout = out.StdoutWriter()
	cmd.Stderr = out.StderrWriter()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	j := m.AdoptCommand("bash", cmd, cancel, "sleep 5", out, waitDone)

	if !m.HasActiveJobs() {
		t.Fatalf("expected active job")
	}
	running, _ := m.Poll(j.ID, 0)
	if running.Status != StatusRunning {
		t.Fatalf("expected running, got %s", running.Status)
	}

	// Kill it
	if err := m.Kill(j.ID); err != nil {
		t.Fatalf("kill: %v", err)
	}
	done, _ := m.Poll(j.ID, 5*time.Second)
	if done.Status != StatusKilled {
		t.Fatalf("expected killed, got %s", done.Status)
	}
}

func TestWaitTimeout(t *testing.T) {
	m := New()
	cmd := exec.Command("sh", "-c", "sleep 5")
	out := &OutputBuffer{}
	cmd.Stdout = out.StdoutWriter()
	cmd.Stderr = out.StderrWriter()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	j := m.AdoptCommand("bash", cmd, func() {}, "sleep 5", out, waitDone)

	// Wait with short timeout; job should still be running
	done, ok := m.Poll(j.ID, 100*time.Millisecond)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != StatusRunning {
		t.Fatalf("expected running after short wait, got %s", done.Status)
	}
	if err := m.Kill(j.ID); err != nil {
		t.Fatalf("kill: %v", err)
	}
}

func TestOutputBufferDrain(t *testing.T) {
	b := &OutputBuffer{}
	w := b.StdoutWriter()
	_, _ = w.Write([]byte("hello"))
	_, _ = w.Write([]byte(" world"))
	stdout, _ := b.Drain()
	if stdout != "hello world" {
		t.Fatalf("unexpected: %q", stdout)
	}
	// After drain, buffer is empty
	stdout, _ = b.Drain()
	if stdout != "" {
		t.Fatalf("expected empty after drain, got %q", stdout)
	}
}

func TestHasActiveJobsFalseAfterOnlyJobFinishesDuringWait(t *testing.T) {
	m := New()
	cmd := exec.Command("sh", "-c", "sleep 0.3")
	out := &OutputBuffer{}
	cmd.Stdout = out.StdoutWriter()
	cmd.Stderr = out.StderrWriter()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	j := m.AdoptCommand("bash", cmd, func() {}, "sleep 0.3", out, waitDone)

	if !m.HasActiveJobs() {
		t.Fatalf("expected active job before completion")
	}

	// Poll waits for the job to finish (0.3s sleep).
	done, ok := m.Poll(j.ID, 5*time.Second)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != StatusDone {
		t.Fatalf("expected done, got %s", done.Status)
	}

	// After the only job finishes during Wait, HasActiveJobs must be false.
	if m.HasActiveJobs() {
		t.Fatalf("expected no active jobs after the only job finished during Wait")
	}
}

func TestAdoptWaitDelayCountsAsDone(t *testing.T) {
	m := New()
	cmd := exec.Command("sh", "-c", "true")
	out := &OutputBuffer{}
	waitDone := make(chan error, 1)
	waitDone <- exec.ErrWaitDelay
	j := m.AdoptCommand("bash", cmd, func() {}, "true &", out, waitDone)

	done, ok := m.Poll(j.ID, 5*time.Second)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != StatusDone || done.ExitCode != 0 {
		t.Fatalf("expected done with exit code 0, got %s/%d", done.Status, done.ExitCode)
	}
}

func TestStartTaskReturnsPersistentResultAndIndependentIDs(t *testing.T) {
	m := New()
	first := m.StartTask("task", "first", func(context.Context) (string, error) {
		return "first result", nil
	})
	second := m.StartTask("task", "second", func(context.Context) (string, error) {
		return "second result", nil
	})
	other := m.StartTask("worker", "other", func(context.Context) (string, error) {
		return "other result", nil
	})
	if first.ID != "task_0" || second.ID != "task_1" || other.ID != "worker_0" {
		t.Fatalf("IDs = %q, %q, %q", first.ID, second.ID, other.ID)
	}
	done, ok := m.Poll(first.ID, time.Second)
	if !ok || done.Kind != KindTask || done.Status != StatusDone || done.Result != "first result" {
		t.Fatalf("first task = %#v, ok = %v", done, ok)
	}
	again, ok := m.Poll(first.ID, 0)
	if !ok || again.Result != "first result" {
		t.Fatalf("repeated poll = %#v, ok = %v", again, ok)
	}
}

func TestKillTaskCancelsItsContext(t *testing.T) {
	m := New()
	started := make(chan struct{})
	task := m.StartTask("task", "wait", func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	<-started
	commands, tasks := m.ActiveCounts()
	if commands != 0 || tasks != 1 {
		t.Fatalf("active counts = commands %d, tasks %d", commands, tasks)
	}
	if err := m.Kill(task.ID); err != nil {
		t.Fatal(err)
	}
	done, ok := m.Poll(task.ID, time.Second)
	if !ok || done.Status != StatusKilled {
		t.Fatalf("killed task = %#v, ok = %v", done, ok)
	}
}

func TestTaskFailureIsRetained(t *testing.T) {
	m := New()
	task := m.StartTask("task", "fail", func(context.Context) (string, error) {
		return "", errors.New("broken")
	})
	done, ok := m.Poll(task.ID, time.Second)
	if !ok || done.Status != StatusFailed || done.Error != "broken" {
		t.Fatalf("failed task = %#v, ok = %v", done, ok)
	}
}
