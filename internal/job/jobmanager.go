// Package job provides background job management for commands and Agent tasks.
package job

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/lgxz/dora/internal/capstr"
)

// Status describes the lifecycle state of a background job.
type Status string

const (
	StatusRunning    Status = "running"
	StatusCancelling Status = "cancelling"
	StatusDone       Status = "done"
	StatusFailed     Status = "failed"
	StatusTimedOut   Status = "timed_out"
	StatusKilled     Status = "killed"
)

// Kind identifies the internal result shape of a job. It is used by the CLI
// and job tool but is not exposed in their JSON protocol; job IDs use the
// source tool name as their prefix instead.
type Kind uint8

const (
	KindCommand Kind = iota
	KindTask
)

// Job is one background command or Agent task.
type Job struct {
	ID string

	mu          sync.Mutex
	kind        Kind
	source      string
	description string
	status      Status
	exitCode    int
	startedAt   time.Time
	finishedAt  time.Time
	result      string
	errText     string
	cancel      context.CancelFunc
	done        chan struct{}
	out         *OutputBuffer
	killed      bool
}

// Snapshot is a concurrency-safe view of a Job. Command stdout/stderr are
// populated only by Manager.Poll, which drains unread command output.
type Snapshot struct {
	ID          string
	Kind        Kind
	Source      string
	Description string
	Status      Status
	ExitCode    int
	StartedAt   time.Time
	FinishedAt  time.Time
	Stdout      string
	Stderr      string
	Result      string
	Error       string
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
	mu      sync.Mutex
	jobs    map[string]*Job
	nextIDs map[string]int
}

// New creates an empty job manager.
func New() *Manager {
	return &Manager{jobs: make(map[string]*Job), nextIDs: make(map[string]int)}
}

// AdoptCommand takes over an already-started process and tracks it as a
// background job. The process is NOT restarted; it continues running.
// waitDone receives the result of cmd.Wait() from the caller.
func (m *Manager) AdoptCommand(
	source string,
	cmd *exec.Cmd,
	cancel context.CancelFunc,
	description string,
	out *OutputBuffer,
	waitDone <-chan error,
) *Job {
	job := m.register(source, KindCommand, description, cancel, out)

	go func() {
		defer close(job.done)
		err := <-waitDone
		job.mu.Lock()
		defer job.mu.Unlock()
		job.finishedAt = time.Now()
		switch {
		case job.killed:
			job.status = StatusKilled
			job.exitCode = -1
		case err == nil, errors.Is(err, exec.ErrWaitDelay):
			// ErrWaitDelay means the process exited successfully but an
			// orphaned child still holds the output pipes.
			job.status = StatusDone
			job.exitCode = 0
		case errors.Is(cmd.Err, context.Canceled):
			job.status = StatusKilled
			job.exitCode = -1
		default:
			job.status = StatusFailed
			if ee, ok := err.(*exec.ExitError); ok {
				job.exitCode = ee.ExitCode()
			} else {
				job.exitCode = -1
			}
		}
	}()
	return job
}

// StartTask runs work in an independent cancellable context and tracks its
// final result in memory. The task cannot outlive the Dora process.
func (m *Manager) StartTask(source, description string, run func(context.Context) (string, error)) *Job {
	return m.StartTaskContext(context.Background(), source, description, run)
}

// StartTaskContext runs work in an independently cancellable context that
// retains values from parent while deliberately dropping its cancellation and
// deadline. This lets background tasks inherit per-run execution settings.
func (m *Manager) StartTaskContext(parent context.Context, source, description string, run func(context.Context) (string, error)) *Job {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	job := m.register(source, KindTask, description, cancel, nil)
	go func() {
		defer close(job.done)
		result, err := run(ctx)
		job.mu.Lock()
		defer job.mu.Unlock()
		job.finishedAt = time.Now()
		switch {
		case job.killed:
			job.status = StatusKilled
		case err != nil:
			job.status = StatusFailed
			job.errText = err.Error()
		default:
			job.status = StatusDone
			job.result = result
		}
	}()
	return job
}

func (m *Manager) register(source string, kind Kind, description string, cancel context.CancelFunc, out *OutputBuffer) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.nextIDs[source]
	m.nextIDs[source] = next + 1
	id := fmt.Sprintf("%s_%d", source, next)
	job := &Job{
		ID:          id,
		kind:        kind,
		source:      source,
		description: description,
		status:      StatusRunning,
		startedAt:   time.Now(),
		cancel:      cancel,
		done:        make(chan struct{}),
		out:         out,
	}
	m.jobs[id] = job
	return job
}

// Kill requests cancellation of a running job and returns its current state.
// A command is killed as a process tree. A Task must cooperate with context
// cancellation, so it may remain in StatusCancelling until its function exits.
func (m *Manager) Kill(id string) (Snapshot, error) {
	m.mu.Lock()
	job, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return Snapshot{}, fmt.Errorf("job %q not found", id)
	}
	job.mu.Lock()
	var cancel context.CancelFunc
	if job.status == StatusRunning {
		job.killed = true
		job.status = StatusCancelling
		cancel = job.cancel
	}
	job.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return job.snapshot(false), nil
}

// CancelAll requests cancellation of every running job. It does not wait for
// cooperative Tasks to return; callers may observe them as cancelling until
// their goroutines finish.
func (m *Manager) CancelAll() {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.Unlock()
	for _, job := range jobs {
		job.mu.Lock()
		var cancel context.CancelFunc
		if job.status == StatusRunning {
			job.killed = true
			job.status = StatusCancelling
			cancel = job.cancel
		}
		job.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

// List returns concurrency-safe snapshots of all tracked jobs, ordered by ID.
func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.Unlock()
	snapshots := make([]Snapshot, 0, len(jobs))
	for _, job := range jobs {
		snapshots = append(snapshots, job.snapshot(false))
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].ID < snapshots[j].ID })
	return snapshots
}

// Poll blocks until the job finishes or the timeout elapses, then returns a
// snapshot. Poll drains unread stdout/stderr for command jobs; Task results
// remain available on repeated polls.
func (m *Manager) Poll(id string, timeout time.Duration) (Snapshot, bool) {
	m.mu.Lock()
	job, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return Snapshot{}, false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-job.done:
	case <-timer.C:
	}
	return job.snapshot(true), true
}

func (j *Job) snapshot(drainCommandOutput bool) Snapshot {
	j.mu.Lock()
	snapshot := Snapshot{
		ID:          j.ID,
		Kind:        j.kind,
		Source:      j.source,
		Description: j.description,
		Status:      j.status,
		ExitCode:    j.exitCode,
		StartedAt:   j.startedAt,
		FinishedAt:  j.finishedAt,
		Result:      j.result,
		Error:       j.errText,
	}
	out := j.out
	kind := j.kind
	j.mu.Unlock()
	if drainCommandOutput && kind == KindCommand && out != nil {
		stdout, stderr := out.Drain()
		snapshot.Stdout = CapOutput(stdout)
		snapshot.Stderr = CapOutput(stderr)
	}
	return snapshot
}

// MaxResultBytes caps the stdout or stderr one command result returns to the
// model. Output beyond the cap is truncated head+tail with a marker directing
// the model to redirect to a file, so a single oversized command cannot
// overflow the model request or the context budget.
const MaxResultBytes = 32 << 10

// The retained output is split between head and tail; the tail keeps late
// errors and summaries visible.
const capHeadBytes = 24 << 10

// CapOutput truncates command output to at most MaxResultBytes (plus the
// marker), keeping the head and tail.
func CapOutput(s string) string {
	return capstr.HeadTail(s, MaxResultBytes, capHeadBytes,
		"\n... [truncated: %d bytes total, showing head+tail; redirect output to a file to inspect all of it] ...\n")
}

// HasActiveJobs reports whether any job is still running.
func (m *Manager) HasActiveJobs() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		job.mu.Lock()
		running := job.status == StatusRunning || job.status == StatusCancelling
		job.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}

// ActiveCounts reports the number of running command and Task jobs.
func (m *Manager) ActiveCounts() (commands, tasks int) {
	m.mu.Lock()
	jobs := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	m.mu.Unlock()
	for _, job := range jobs {
		snapshot := job.snapshot(false)
		if snapshot.Status != StatusRunning && snapshot.Status != StatusCancelling {
			continue
		}
		if snapshot.Kind == KindTask {
			tasks++
		} else {
			commands++
		}
	}
	return commands, tasks
}
