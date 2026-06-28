package transfer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordingRunner returns a Runner that copies progress through and
// optionally fails the first failFirst attempts of a given job key.
func recordingRunner(total int64, failFirst int) (Runner, *int32) {
	var calls int32
	return func(src, dst string, prog func(done, total int64)) (int64, error) {
		n := atomic.AddInt32(&calls, 1)
		if prog != nil {
			// simulate streaming bytes
			prog(total/2, total)
			prog(total, total)
		}
		if int(n) <= failFirst {
			return 0, errors.New("transient")
		}
		return total, nil
	}, &calls
}

func TestQueue_RunsInOrderAndDrains(t *testing.T) {
	q := NewQueue(0)
	var mu sync.Mutex
	order := []string{}
	mkRun := func(key string) Runner {
		return func(src, dst string, prog func(done, total int64)) (int64, error) {
			mu.Lock()
			order = append(order, key)
			mu.Unlock()
			if prog != nil {
				prog(10, 10)
			}
			return 10, nil
		}
	}
	completed := make(chan Completed, 4)
	// Each job needs its own runner, but Queue.Run takes one Runner. We
	// route by src in a single runner.
	runner := func(src, dst string, prog func(done, total int64)) (int64, error) {
		return mkRun(src)(src, dst, prog)
	}

	q.Enqueue(
		&Job{Src: "a", Direction: DirDownload, Size: 10},
		&Job{Src: "b", Direction: DirDownload, Size: 10},
		&Job{Src: "c", Direction: DirUpload, Size: 10},
	)
	if got := q.Len(); got != 3 {
		t.Fatalf("Len after enqueue = %d, want 3", got)
	}

	if err := q.Run(context.Background(), runner, nil, completed); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Fatalf("order = %v, want [a b c]", order)
	}
	if q.Len() != 0 {
		t.Fatalf("queue should be drained, Len=%d", q.Len())
	}
	if len(completed) != 3 {
		t.Fatalf("completed = %d, want 3", len(completed))
	}
	// all completed jobs should be StatusDone
	for c := range completed {
		// drain
		if c.Job.Status != StatusDone {
			t.Fatalf("job %s status = %v", c.Job.Src, c.Job.Status)
		}
		if len(completed) == 0 {
			break
		}
	}
}

func TestQueue_RetriesThenSucceeds(t *testing.T) {
	q := NewQueue(2)                        // allow up to 2 retries
	runner, calls := recordingRunner(50, 1) // fails once then succeeds

	q.Enqueue(&Job{Src: "flaky", Dst: "out", Direction: DirDownload, Size: 50})
	err := q.Run(context.Background(), runner, nil, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("expected 2 attempts (1 fail + 1 success), got %d", n)
	}
	snap := q.Snapshot()
	if len(snap) != 0 {
		// drained on success
	}
}

func TestQueue_FailsAfterMaxRetry(t *testing.T) {
	q := NewQueue(1)                         // 1 retry
	runner, calls := recordingRunner(50, 99) // always fails

	completed := make(chan Completed, 1)
	q.Enqueue(&Job{Src: "bad", Dst: "out", Direction: DirUpload, Size: 50})
	if err := q.Run(context.Background(), runner, nil, completed); err != nil {
		t.Fatalf("Run returns nil even with failed jobs: %v", err)
	}
	// 2 attempts: original + 1 retry
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("expected 2 attempts, got %d", n)
	}
	c := <-completed
	if c.Job.Status != StatusFailed || c.Job.Err == "" {
		t.Fatalf("expected failed job with Err, got %+v", c.Job)
	}
}

func TestQueue_ProgressThrottledAndFinal(t *testing.T) {
	q := NewQueue(0)
	// many rapid progress updates; final one must always arrive
	runner := func(src, dst string, prog func(done, total int64)) (int64, error) {
		for i := int64(1); i <= 200; i++ {
			prog(i, 200)
		}
		return 200, nil
	}
	progress := make(chan Progress, 256)
	q.Enqueue(&Job{Src: "s", Dst: "d", Direction: DirDownload, Size: 200})

	go func() { _ = q.Run(context.Background(), runner, progress, nil) }()

	var last Progress
	var got int
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
drain:
	for {
		select {
		case p := <-progress:
			last = p
			got++
		case <-timer.C:
			t.Fatal("timed out waiting for run to finish")
		default:
			if q.Len() == 0 {
				break drain
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	if got < 1 {
		t.Fatal("no progress emitted")
	}
	if last.Done != last.Total || last.Total != 200 {
		t.Fatalf("final progress = %d/%d, want 200/200", last.Done, last.Total)
	}
}

func TestQueue_Cancellation(t *testing.T) {
	q := NewQueue(0)
	started := make(chan struct{}, 1)
	runner := func(src, dst string, prog func(done, total int64)) (int64, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		time.Sleep(50 * time.Millisecond)
		return 1, nil
	}
	q.Enqueue(&Job{Src: "s", Dst: "d", Direction: DirDownload})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	err := q.Run(ctx, runner, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestQueue_PendingAndClear(t *testing.T) {
	q := NewQueue(0)
	q.Enqueue(&Job{Src: "a"}, &Job{Src: "b"})
	if p := q.Pending(); p != 2 {
		t.Fatalf("Pending = %d, want 2", p)
	}
	q.Clear()
	if q.Len() != 0 || q.Pending() != 0 {
		t.Fatalf("after Clear, Len=%d Pending=%d", q.Len(), q.Pending())
	}
}

func TestDirectionString(t *testing.T) {
	if DirDownload.String() != "download" || DirUpload.String() != "upload" {
		t.Fatal("direction string labels wrong")
	}
}
