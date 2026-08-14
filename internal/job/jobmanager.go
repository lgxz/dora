// Package job provides background job management for long-running commands.
package job

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Status describes the lifecycle state of a background job.
type Status string

const (
	StatusRunning  Status = "running"
	StatusDone     Status = "done"
	StatusFailed   Status = "failed"
	StatusTimedOut Status = "timed_out"
	StatusKilled   Status = "killed"
)

// Job is a background process adopted from a foreground command.
type Job struct {
	ID         string
	Command    string
	Status     Status
	ExitCode   int
	StartedAt  time.Time
	FinishedAt time.Time

	mu      sync.Mutex
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	done    chan struct{}
	out     *OutputBuffer
	killed  bool
}

// DrainOutput returns the job's unread output and clears the buffer.
func (j *Job) DrainOutput() (stdout, stderr string) {
	if j.out == nil {
		return "", ""
	}
	return j.out.Drain()
}

// OutputBuffer is a concurrency-safe output buffer that is drained (read and
// cleared) after each read, so it only holds unread output.
type OutputBuffer struct {
	mu     sync.Mutex
	stdout bytes.Buffer
	stderr bytes.Buffer
}

// StdoutWriter returns an io.Writer that appends to the stdout buffer.
func (b *OutputBuffer) StdoutWriter() io.Writer {
	return &bufferWriter{b: b, stderr: false}
}

// StderrWriter returns an io.Writer that appends to the stderr buffer.
func (b *OutputBuffer) StderrWriter() io.Writer {
	return &bufferWriter{b: b, stderr: true}
}

type bufferWriter struct {
	b      *OutputBuffer
	stderr bool
}

func (w *bufferWriter) Write(p []byte) (int, error) {
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	if w.stderr {
		return w.b.stderr.Write(p)
	}
	return w.b.stdout.Write(p)
}

// Drain returns the current stdout and stderr and clears the buffer.
func (b *OutputBuffer) Drain() (stdout, stderr string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.stdout.String()
	err := b.stderr.String()
	b.stdout.Reset()
	b.stderr.Reset()
	return out, err
}

// Manager tracks background jobs.
type Manager struct {
	mu     sync.Mutex
	jobs   map[string]*Job
	nextID int
}

// New creates an empty job manager.
func New() *Manager {
	return &Manager{jobs: make(map[string]*Job)}
}

// Adopt takes over an already-started process and tracks it as a background
// job. The process is NOT restarted; it continues running. waitDone is the
// channel that receives the result of cmd.Wait() (set up by the caller).
func (m *Manager) Adopt(
	cmd *exec.Cmd,
	cancel context.CancelFunc,
	command string,
	out *OutputBuffer,
	waitDone <-chan error,
) *Job {
	m.mu.Lock()
	id := fmt.Sprintf("job_%d", m.nextID)
	m.nextID++
	job := &Job{
		ID:        id,
		Command:   command,
		Status:    StatusRunning,
		StartedAt: time.Now(),
		cmd:       cmd,
		cancel:    cancel,
		done:      make(chan struct{}),
		out:       out,
	}
	m.jobs[id] = job
	m.mu.Unlock()

	go func() {
		defer close(job.done)
		err := <-waitDone
		job.mu.Lock()
		defer job.mu.Unlock()
		job.FinishedAt = time.Now()
		switch {
		case err == nil:
			job.Status = StatusDone
			job.ExitCode = 0
		case job.killed:
			job.Status = StatusKilled
			job.ExitCode = -1
		case errors.Is(cmd.Err, context.Canceled):
			job.Status = StatusKilled
			job.ExitCode = -1
		default:
			job.Status = StatusFailed
			if ee, ok := err.(*exec.ExitError); ok {
				job.ExitCode = ee.ExitCode()
			} else {
				job.ExitCode = -1
			}
		}
	}()
	return job
}

// Status returns a job by ID.
func (m *Manager) Status(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	return job, ok
}

// Kill terminates a running job.
func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	job, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("job %q not found", id)
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.Status != StatusRunning {
		return nil
	}
	job.killed = true
	if job.cancel != nil {
		job.cancel()
	}
	return nil
}

// List returns all tracked jobs.
func (m *Manager) List() []*Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	return jobs
}

// Wait blocks until the job finishes or the timeout elapses, then returns the
// job. It is used by the job tool's poll action.
func (m *Manager) Wait(id string, timeout time.Duration) (*Job, bool) {
	m.mu.Lock()
	job, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return nil, false
	}
	select {
	case <-job.done:
	case <-time.After(timeout):
	}
	return job, true
}

// HasActiveJobs reports whether any job is still running.
func (m *Manager) HasActiveJobs() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		job.mu.Lock()
		running := job.Status == StatusRunning
		job.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}

// Cleanup kills all running jobs. Call at session end.
func (m *Manager) Cleanup() {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.Unlock()
	for _, job := range jobs {
		job.mu.Lock()
		if job.Status == StatusRunning {
			job.killed = true
			if job.cancel != nil {
				job.cancel()
			}
		}
		job.mu.Unlock()
	}
}
