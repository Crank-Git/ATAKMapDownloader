package commands

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/jobs"
)

// stallTimeout is the time that a test waits for a goroutine that must not block.
const stallTimeout = 3 * time.Second

// newBareJob returns a running job with no engine, so the test sends its progress and its result.
// Its store is a jobs.jsonl file in a new directory.
func newBareJob(t *testing.T, id string) *job {
	t.Helper()
	store, err := jobs.OpenStore(filepath.Join(t.TempDir(), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return &job{
		id:        id,
		store:     store,
		cancel:    func() {},
		progress:  make(chan progressEvent, 64),
		result:    make(chan jobResult, 1),
		subscribe: make(chan *subscriber),
		done:      make(chan struct{}),
		record:    jobRecord{ID: id, State: jobRunning, Total: 100},
	}
}

func TestAStalledSubscriberDoesNotStopTheJobFromItsFinalState(t *testing.T) {
	j := newBareJob(t, "stalled")
	go j.sendEvents()

	// The stalled subscriber never reads and its stream never ends, as a client that stops reading.
	stalled := &subscriber{events: make(chan sseMessage, 3), gone: make(chan struct{})}
	defer close(stalled.gone)
	j.subscribe <- stalled

	// More progress ticks than the buffer holds fill the buffer of the stalled subscriber.
	for i := 1; i <= 2*cap(stalled.events); i++ {
		j.progress <- progressEvent{Downloaded: int64(i), Total: 100}
		time.Sleep(progressInterval + progressInterval/2)
	}
	j.result <- jobResult{state: jobComplete}

	select {
	case <-j.done:
	case <-time.After(stallTimeout):
		t.Fatal("the event goroutine is blocked on the stalled subscriber, so the job has no final state")
	}
	if got := j.snapshot().State; got != jobComplete {
		t.Fatalf("state = %q, want %q", got, jobComplete)
	}
	last := lastMessage(stalled.events)
	if last.name != "state" || last.data.(jobRecord).State != jobComplete {
		t.Fatalf("last event of the stalled subscriber = %+v, want the final state event", last)
	}
}

// lastMessage returns the last message in events. The caller makes sure that the sender closed events.
func lastMessage(events chan sseMessage) sseMessage {
	var last sseMessage
	for m := range events {
		last = m
	}
	return last
}

func TestAStreamWithACancelledRequestReturnsWhileTheEventGoroutineIsBusy(t *testing.T) {
	h := newTestHandler(t, nil)
	// No event goroutine runs, so a subscribe send waits as it does for a busy event goroutine.
	j := newBareJob(t, "busy")
	h.jobs[j.id] = j

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, "GET", "/api/jobs/busy/events", nil)
	req.SetPathValue("id", "busy")
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		h.streamJob(httptest.NewRecorder(), req)
	}()
	cancel()

	select {
	case <-returned:
	case <-time.After(stallTimeout):
		t.Fatal("streamJob stays blocked on the subscribe send after the client left")
	}
}
