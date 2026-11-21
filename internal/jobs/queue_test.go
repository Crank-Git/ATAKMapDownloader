package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitTimeout is the time that a test waits for a worker to start a job.
const waitTimeout = 3 * time.Second

// quietTime is the time that a test waits to see that no worker starts a job.
const quietTime = 100 * time.Millisecond

// blockingRun is a run func that reports each start and waits for release.
type blockingRun struct {
	started chan string
	release chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func newBlockingRun() *blockingRun {
	return &blockingRun{started: make(chan string, MaxQueued), release: make(chan struct{})}
}

func (b *blockingRun) run(id string) {
	n := b.active.Add(1)
	for {
		p := b.peak.Load()
		if n <= p || b.peak.CompareAndSwap(p, n) {
			break
		}
	}
	b.started <- id
	<-b.release
	b.active.Add(-1)
}

// next returns the id of the next job that a worker starts.
func (b *blockingRun) next(t *testing.T) string {
	t.Helper()
	select {
	case id := <-b.started:
		return id
	case <-time.After(waitTimeout):
		t.Fatal("no worker started a job")
		return ""
	}
}

// none fails the test when a worker starts a job within quietTime.
func (b *blockingRun) none(t *testing.T) {
	t.Helper()
	select {
	case id := <-b.started:
		t.Fatalf("a worker started %s, want no start", id)
	case <-time.After(quietTime):
	}
}

// addAll adds each id to q, and fails the test when an Add returns an error.
func addAll(t *testing.T, q *Queue, ids []string) {
	t.Helper()
	for _, id := range ids {
		if _, err := q.Add(id); err != nil {
			t.Fatalf("Add(%s) returned %v", id, err)
		}
	}
}

func TestQueueRunsAtMostWorkersJobsInAddOrder(t *testing.T) {
	tests := []struct {
		name    string
		workers int
	}{
		{"one worker runs one job at a time", 1},
		{"two workers run two jobs at a time", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBlockingRun()
			q := NewQueue(b.run)
			ids := []string{"a", "b", "c", "d", "e"}
			addAll(t, q, ids)
			q.Start(t.Context(), tt.workers)

			var got []string
			for range tt.workers {
				got = append(got, b.next(t))
			}
			b.none(t)
			close(b.release)
			for len(got) < len(ids) {
				got = append(got, b.next(t))
			}
			if peak := int(b.peak.Load()); peak != tt.workers {
				t.Errorf("peak of running jobs = %d, want %d", peak, tt.workers)
			}
			// Two workers can report their starts in either order, so only one worker gives the exact order.
			if tt.workers == 1 && !slices.Equal(got, ids) {
				t.Errorf("start order = %v, want %v", got, ids)
			}
		})
	}
}

func TestQueueAddAboveMaxQueuedReturnsErrQueueFull(t *testing.T) {
	q := NewQueue(func(string) {})
	for i := 1; i <= MaxQueued; i++ {
		pos, err := q.Add(fmt.Sprint(i))
		if err != nil || pos != i {
			t.Fatalf("Add of job %d = %d, %v, want position %d", i, pos, err, i)
		}
	}
	if _, err := q.Add("over"); !errors.Is(err, ErrQueueFull) {
		t.Errorf("Add of job %d returned %v, want ErrQueueFull", MaxQueued+1, err)
	}
	if q.Position("over") != 0 {
		t.Error("the rejected job has a position")
	}
}

func TestQueueRemoveAndPosition(t *testing.T) {
	b := newBlockingRun()
	q := NewQueue(b.run)
	addAll(t, q, []string{"a", "b", "c"})
	q.Start(t.Context(), 1)
	if id := b.next(t); id != "a" {
		t.Fatalf("first start = %s, want a", id)
	}

	tests := []struct {
		name     string
		id       string
		position int
		removed  bool
	}{
		{"a running job has no position and stays", "a", 0, false},
		{"the next queued job has position 1", "b", 1, true},
		{"an unknown job has no position", "x", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := q.Position(tt.id); got != tt.position {
				t.Errorf("Position(%s) = %d, want %d", tt.id, got, tt.position)
			}
			if got := q.Remove(tt.id); got != tt.removed {
				t.Errorf("Remove(%s) = %v, want %v", tt.id, got, tt.removed)
			}
		})
	}
	if got := q.Position("c"); got != 1 {
		t.Errorf("Position(c) after the remove of b = %d, want 1", got)
	}

	// The removed job b never runs, so c is the next start.
	close(b.release)
	if id := b.next(t); id != "c" {
		t.Errorf("start after the remove = %s, want c", id)
	}
}

func TestQueueWorkersStopWhenTheContextEnds(t *testing.T) {
	var mu sync.Mutex
	var ran []string
	q := NewQueue(func(id string) {
		mu.Lock()
		defer mu.Unlock()
		ran = append(ran, id)
	})
	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx, 2)
	cancel()
	// The workers select on the context, so a job that comes after the end never runs.
	time.Sleep(quietTime)
	if _, err := q.Add("late"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(quietTime)
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 0 {
		t.Errorf("jobs %v ran after the context ended", ran)
	}
}
