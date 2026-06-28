// Package transfer runs a sequential background queue of file transfers
// (built on the sftp.Backend interface), reporting progress so the UI can
// show a progress bar and queue depth.
package transfer

import (
	"context"
	"sync"
	"time"
)

// Direction tells which way a job moves files.
type Direction int

const (
	DirDownload Direction = iota // remote -> local
	DirUpload                    // local -> remote
)

// String returns a human-readable direction label.
func (d Direction) String() string {
	switch d {
	case DirDownload:
		return "download"
	case DirUpload:
		return "upload"
	default:
		return "?"
	}
}

// Status is the lifecycle state of a job.
type Status int

const (
	StatusQueued Status = iota
	StatusRunning
	StatusDone
	StatusFailed
)

// Job describes one file transfer between two sftp.Backend paths.
type Job struct {
	Src, Dst  string
	Direction Direction
	Size      int64 // best-effort, filled from Stat at run time
	Done      int64
	Status    Status
	Err       string // last error message if Status == StatusFailed
}

// Progress is emitted through the progress channel while a job runs.
type Progress struct {
	Job       *Job
	Done      int64
	Total     int64
	QueueLeft int // jobs remaining after the current one (excludes running)
}

// Completed is emitted when each job finishes (done or failed).
type Completed struct {
	Job *Job
}

// Queue is a sequential, single-runner job queue. It is safe to Enqueue from
// one goroutine while Run executes in another.
type Queue struct {
	mu       sync.Mutex
	jobs     []*Job
	running  bool
	maxRetry int
}

// NewQueue returns a queue that retries each failed job maxRetry times.
func NewQueue(maxRetry int) *Queue {
	if maxRetry < 0 {
		maxRetry = 0
	}
	return &Queue{maxRetry: maxRetry}
}

// Enqueue appends jobs in StatusQueued and returns the new queue length.
func (q *Queue) Enqueue(jobs ...*Job) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range jobs {
		if j == nil {
			continue
		}
		j.Status = StatusQueued
		j.Done = 0
		q.jobs = append(q.jobs, j)
	}
	return len(q.jobs)
}

// Len reports total jobs currently held (any status).
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

// Pending reports jobs not yet finished (queued or running).
func (q *Queue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, j := range q.jobs {
		if j.Status == StatusQueued || j.Status == StatusRunning {
			n++
		}
	}
	return n
}

// Snapshot returns a shallow copy of all jobs for UI rendering.
func (q *Queue) Snapshot() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Job, len(q.jobs))
	for i, j := range q.jobs {
		out[i] = *j
	}
	return out
}

// Runner executes a single transfer: it returns the byte count and any error.
// The queue calls it per job so the transport is injectable for tests.
type Runner func(src, dst string, prog func(done, total int64)) (int64, error)

// Run processes every queued job in order until the queue is drained or ctx
// is cancelled. progress is sent current-job updates (throttled); completed is
// sent once per job when it finishes. Both channels are optional (nil-safe).
// Run drains the queue: finished jobs are removed as they complete.
func (q *Queue) Run(ctx context.Context, run Runner, progress chan<- Progress, completed chan<- Completed) error {
	q.mu.Lock()
	if q.running {
		q.mu.Unlock()
		return nil
	}
	q.running = true
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.running = false
		q.mu.Unlock()
	}()

	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		q.mu.Lock()
		// find next queued job
		var job *Job
		idx := -1
		for i, j := range q.jobs {
			if j.Status == StatusQueued {
				job, idx = j, i
				break
			}
		}
		queueLeft := 0
		for i := idx + 1; i < len(q.jobs); i++ {
			if q.jobs[i].Status == StatusQueued {
				queueLeft++
			}
		}
		if job == nil {
			q.mu.Unlock()
			return nil // nothing left
		}
		job.Status = StatusRunning
		q.mu.Unlock()

		// Throttle progress to at most every 100ms to avoid flooding the UI.
		var lastEmit time.Time
		emit := func(force bool) {
			if progress == nil {
				return
			}
			now := time.Now()
			if !force && now.Sub(lastEmit) < 100*time.Millisecond {
				return
			}
			lastEmit = now
			progress <- Progress{Job: job, Done: job.Done, Total: job.Size, QueueLeft: queueLeft}
		}

		exec := func() error {
			job.Done = 0
			n, err := run(job.Src, job.Dst, func(done, total int64) {
				job.Done = done
				if total > job.Size {
					job.Size = total
				}
				emit(false)
			})
			if n > job.Done {
				job.Done = n
			}
			return err
		}

		err := exec()
		if err != nil {
			// retry up to maxRetry times
			retries := 0
			for err != nil && retries < q.maxRetry {
				retries++
				err = exec()
			}
		}
		if err != nil {
			job.Status = StatusFailed
			job.Err = err.Error()
		} else {
			job.Status = StatusDone
			job.Done = job.Size
		}
		emit(true) // final update for this job
		if completed != nil {
			completed <- Completed{Job: job}
		}
		// remove finished job from the slice
		q.mu.Lock()
		q.jobs = append(q.jobs[:idx], q.jobs[idx+1:]...)
		q.mu.Unlock()
	}
}

// Clear removes all jobs (used on disconnect / view reset).
func (q *Queue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = nil
}
