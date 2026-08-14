package job

import (
	"context"
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
	j := m.Adopt(cmd, func() {}, "echo hello; sleep 0.2; echo world", out, waitDone)

	// Wait for completion
	done, ok := m.Wait(j.ID, 5*time.Second)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != StatusDone {
		t.Fatalf("expected done, got %s", done.Status)
	}
	if done.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", done.ExitCode)
	}
	stdout, _ := done.DrainOutput()
	if stdout != "hello\nworld\n" {
		t.Fatalf("unexpected stdout: %q", stdout)
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
	j := m.Adopt(cmd, cancel, "sleep 5", out, waitDone)

	if !m.HasActiveJobs() {
		t.Fatalf("expected active job")
	}
	if j.Status != StatusRunning {
		t.Fatalf("expected running, got %s", j.Status)
	}

	// Kill it
	if err := m.Kill(j.ID); err != nil {
		t.Fatalf("kill: %v", err)
	}
	done, _ := m.Wait(j.ID, 5*time.Second)
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
	j := m.Adopt(cmd, func() {}, "sleep 5", out, waitDone)

	// Wait with short timeout; job should still be running
	done, ok := m.Wait(j.ID, 100*time.Millisecond)
	if !ok {
		t.Fatalf("job not found")
	}
	if done.Status != StatusRunning {
		t.Fatalf("expected running after short wait, got %s", done.Status)
	}
	m.Cleanup()
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
	j := m.Adopt(cmd, func() {}, "sleep 0.3", out, waitDone)

	if !m.HasActiveJobs() {
		t.Fatalf("expected active job before completion")
	}

	// Poll waits for the job to finish (0.3s sleep).
	done, ok := m.Wait(j.ID, 5*time.Second)
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

func TestCleanup(t *testing.T) {
	m := New()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 10")
	out := &OutputBuffer{}
	cmd.Stdout = out.StdoutWriter()
	cmd.Stderr = out.StderrWriter()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	j := m.Adopt(cmd, cancel, "sleep 10", out, waitDone)
	m.Cleanup()
	// Wait for the job to actually finish (cancel is async)
	done, _ := m.Wait(j.ID, 5*time.Second)
	if done.Status != StatusKilled {
		t.Fatalf("expected killed, got %s", done.Status)
	}
	if m.HasActiveJobs() {
		t.Fatalf("expected no active jobs after cleanup")
	}
}

var _ = context.Background