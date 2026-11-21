package commands

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestAResumeDuringACancelLeavesNoJobQueuedOutsideTheQueue(t *testing.T) {
	s := newJobServer(t, slowTables(time.Second, slowArea, otherArea))
	running := s.startJob(t, jobBody(slowArea))
	s.waitState(t, running.ID, jobRunning)
	// The worker runs the slow job, so a resumed job stays in the queue.
	target := s.startJob(t, jobBody(otherArea))
	if got := s.cancelAndWait(t, target.ID); got.State != jobCancelled {
		t.Fatalf("first cancel: final state = %q, want cancelled", got.State)
	}

	var wg sync.WaitGroup
	resumeStatus := 0
	afterCancelLookup = func() {
		wg.Go(func() {
			resumeStatus = serve(s.handler, http.MethodPost, "/api/jobs/"+target.ID+"/resume", "").Code
		})
		// The resume gets this time to replace the job, unless the cancel holds the job map.
		time.Sleep(100 * time.Millisecond)
	}
	t.Cleanup(func() { afterCancelLookup = func() {} })
	if code := serve(s.handler, http.MethodDelete, "/api/jobs/"+target.ID, "").Code; code != http.StatusAccepted {
		t.Fatalf("DELETE status = %d, want 202", code)
	}
	afterCancelLookup = func() {}
	wg.Wait()
	if resumeStatus != http.StatusAccepted {
		t.Fatalf("resume status = %d, want 202", resumeStatus)
	}

	if got := s.getRecord(t, target.ID); got.State == jobQueued && got.Position == 0 {
		t.Fatalf("resumed job = state %q at position 0, want a job that the queue holds (FR-team-server-4)", got.State)
	}
	if got := s.cancelAndWait(t, target.ID); got.State != jobCancelled {
		t.Errorf("cancel of the resumed job: final state = %q, want cancelled", got.State)
	}
	s.cancelAndWait(t, running.ID)
}
