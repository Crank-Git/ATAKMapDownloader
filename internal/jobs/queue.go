// Package jobs holds the job queue and the job store of the team server (feature 12).
// It knows only job ids, so it has no HTTP code and no engine code.
package jobs

import (
	"context"
	"errors"
	"slices"
	"sync"
)

// MaxQueued is the highest number of queued jobs (FR-team-server-6).
const MaxQueued = 50

// ErrQueueFull is the error of Add when the queue holds MaxQueued jobs.
var ErrQueueFull = errors.New("queue full")

// Queue runs at most the worker count of jobs at the same time, in the order of Add (FR-team-server-2, -3).
// A job is queued from Add until a worker takes it. The queue forgets the job when a worker takes it.
type Queue struct {
	run func(id string)
	// wake has a buffer of 1, so an Add never blocks and a worker that waits sees at least one signal.
	wake chan struct{}

	mu    sync.Mutex
	order []string
}

// NewQueue returns an empty queue. A worker calls run for each job that it takes.
func NewQueue(run func(id string)) *Queue {
	return &Queue{run: run, wake: make(chan struct{}, 1)}
}

// Start starts workers goroutines. Each one takes the next queued job and calls run.
// The workers stop when ctx ends, and a run that started continues. Call Start once.
func (q *Queue) Start(ctx context.Context, workers int) {
	for range workers {
		go q.work(ctx)
	}
}

// work runs queued jobs one at a time until ctx ends.
func (q *Queue) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		}
		// The check comes before take, so a job stays queued when the workers stop.
		for ctx.Err() == nil {
			id, ok := q.take()
			if !ok {
				break
			}
			q.run(id)
		}
	}
}

// take removes the next queued job and returns its id, or false when the queue is empty.
// It signals wake again when jobs stay, because the one signal of Add woke only one worker.
func (q *Queue) take() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		return "", false
	}
	id := q.order[0]
	q.order = q.order[1:]
	if len(q.order) > 0 {
		q.signal()
	}
	return id, true
}

// signal wakes one waiting worker. A full buffer already holds a signal, so the send can drop.
func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Add queues the job id and returns its position, 1 for the next job.
// It returns ErrQueueFull when the queue holds MaxQueued jobs.
func (q *Queue) Add(id string) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) >= MaxQueued {
		return 0, ErrQueueFull
	}
	q.order = append(q.order, id)
	q.signal()
	return len(q.order), nil
}

// Remove removes the queued job id. It returns false when the job is not queued, for example when a worker took it.
func (q *Queue) Remove(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := slices.Index(q.order, id)
	if i < 0 {
		return false
	}
	q.order = slices.Delete(q.order, i, i+1)
	return true
}

// Position returns the position of the queued job id, 1 for the next job, or 0 when the job is not queued.
func (q *Queue) Position(id string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Index(q.order, id) + 1
}
