package commands

import (
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/jobs"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// otherArea is a 2×2 tile area apart from slowArea, so the tile log tells the two jobs apart.
var otherArea = tilecalc.TileBounds{MinX: 320, MaxX: 321, MinY: 400, MaxY: 401, Zoom: 10}

// inArea reports whether tile is a tile of tb.
func inArea(tile testtiles.Tile, tb tilecalc.TileBounds) bool {
	return tile.Z == tb.Zoom && tile.X >= tb.MinX && tile.X <= tb.MaxX && tile.Y >= tb.MinY && tile.Y <= tb.MaxY
}

// areaRequests returns the number of tile requests for the tiles of tb.
func (s *jobServer) areaRequests(tb tilecalc.TileBounds) int {
	n := 0
	for _, r := range s.tiles.Log() {
		if inArea(r.Tile, tb) {
			n++
		}
	}
	return n
}

// slowTables returns one table that delays every tile of each area by delay.
func slowTables(delay time.Duration, areas ...tilecalc.TileBounds) testtiles.Table {
	table := testtiles.Table{}
	for _, a := range areas {
		maps.Copy(table, slowTable(a, delay))
	}
	return table
}

// waitState polls GET /api/jobs/{id} until the job has state, and fails the test after stallTimeout.
func (s *jobServer) waitState(t *testing.T, id string, state jobState) jobRecord {
	t.Helper()
	deadline := time.Now().Add(stallTimeout)
	for {
		rec := s.getRecord(t, id)
		if rec.State == state {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s has state %q, want %q", id, rec.State, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cancelAndWait cancels job id and waits for its final state.
func (s *jobServer) cancelAndWait(t *testing.T, id string) jobRecord {
	t.Helper()
	closeBody(s.request(t, http.MethodDelete, "/api/jobs/"+id, ""))
	return s.finalRecord(t, id)
}

// namedJobBody returns a POST /api/jobs body for the tiles of tb with the output file name name.
func namedJobBody(tb tilecalc.TileBounds, name string) string {
	return strings.TrimSuffix(jobBody(tb), "}") + fmt.Sprintf(`,"name":%q}`, name)
}

func TestTwoJobsRunInOrderWithOneWorker(t *testing.T) {
	s := newJobServer(t, slowTables(50*time.Millisecond, slowArea, otherArea))
	first := s.startJob(t, jobBody(slowArea))
	if first.State != jobQueued {
		t.Errorf("first record state = %q, want queued (FR-team-server-1)", first.State)
	}
	s.waitState(t, first.ID, jobRunning)
	second := s.startJob(t, jobBody(otherArea))
	if second.State != jobQueued || second.Position != 1 {
		t.Errorf("second record = state %q, position %d, want queued at position 1", second.State, second.Position)
	}

	if got := s.finalRecord(t, second.ID); got.State != jobComplete {
		t.Fatalf("second final record = %+v, want complete", got)
	}
	if got := s.finalRecord(t, first.ID); got.State != jobComplete {
		t.Fatalf("first final record = %+v, want complete", got)
	}
	lastFirst, firstSecond := -1, -1
	for i, r := range s.tiles.Log() {
		switch {
		case inArea(r.Tile, slowArea):
			lastFirst = i
		case inArea(r.Tile, otherArea) && firstSecond < 0:
			firstSecond = i
		}
	}
	if lastFirst < 0 || firstSecond < 0 || firstSecond < lastFirst {
		t.Errorf("last tile request of job 1 at %d, first of job 2 at %d, want job 1 to end before job 2 starts", lastFirst, firstSecond)
	}
}

func TestDeleteCancelsAQueuedJobWithNoTileRequest(t *testing.T) {
	s := newJobServer(t, slowTables(100*time.Millisecond, slowArea, otherArea))
	first := s.startJob(t, jobBody(slowArea))
	s.waitState(t, first.ID, jobRunning)
	second := s.startJob(t, jobBody(otherArea))

	resp := s.request(t, http.MethodDelete, "/api/jobs/"+second.ID, "")
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("DELETE status = %d, want 202", resp.StatusCode)
	}
	closeBody(resp)
	if got := s.finalRecord(t, second.ID); got.State != jobCancelled {
		t.Errorf("final state of the queued job = %q, want cancelled", got.State)
	}
	if got := s.getRecord(t, second.ID); got.State != jobCancelled || got.Position != 0 {
		t.Errorf("GET record = state %q, position %d, want cancelled with no position", got.State, got.Position)
	}
	s.cancelAndWait(t, first.ID)
	// The worker is free now, so a job left in the queue would start in this time.
	time.Sleep(200 * time.Millisecond)
	if n := s.areaRequests(otherArea); n != 0 {
		t.Errorf("the cancelled queued job sent %d tile requests, want 0", n)
	}
}

func TestResumeRequestsOnlyTheMissingTiles(t *testing.T) {
	area := tilecalc.TileBounds{MinX: 310, MaxX: 311, MinY: 400, MaxY: 401, Zoom: 10}
	// The main pass and the retry pass each get a 404 for these tiles, so the first run fails with half of the tiles.
	missing := []testtiles.Tile{{Z: 10, X: 310, Y: 400}, {Z: 10, X: 311, Y: 401}}
	table := testtiles.Table{}
	for _, tile := range missing {
		table[tile] = testtiles.Response{Kind: testtiles.NotFound, Times: 2}
	}
	s := newJobServer(t, table)
	rec := s.startJob(t, jobBody(area))
	if got := s.finalRecord(t, rec.ID); got.State != jobFailed || got.Downloaded != 2 {
		t.Fatalf("first final record = %+v, want failed with 2 tiles", got)
	}
	before := s.tiles.TotalRequests()

	resp := s.request(t, http.MethodPost, "/api/jobs/"+rec.ID+"/resume", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("resume status = %d, want 202", resp.StatusCode)
	}
	resumed := decodeRecord(t, resp.Body)
	closeBody(resp)
	if resumed.ID != rec.ID || resumed.OutputFile != rec.OutputFile {
		t.Errorf("resume record = %+v, want the id %s and the output file %s", resumed, rec.ID, rec.OutputFile)
	}

	final := s.finalRecord(t, rec.ID)
	if final.State != jobComplete || final.Skipped != 2 {
		t.Errorf("final record after the resume = %+v, want complete with 2 skipped tiles", final)
	}
	if got := s.tiles.TotalRequests() - before; got != len(missing) {
		t.Errorf("the resume sent %d tile requests, want %d", got, len(missing))
	}
}

func TestResumeOfACancelledJobCompletesItsOutputFile(t *testing.T) {
	s := newJobServer(t, slowTable(slowArea, 50*time.Millisecond))
	rec := s.startJob(t, jobBody(slowArea))
	s.waitState(t, rec.ID, jobRunning)
	if got := s.cancelAndWait(t, rec.ID); got.State != jobCancelled {
		t.Fatalf("state after the cancel = %q, want cancelled", got.State)
	}
	resp := s.request(t, http.MethodPost, "/api/jobs/"+rec.ID+"/resume", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("resume status = %d, want 202", resp.StatusCode)
	}
	closeBody(resp)
	if got := s.finalRecord(t, rec.ID); got.State != jobComplete || got.Downloaded+got.Skipped != 16 {
		t.Errorf("final record after the resume = %+v, want complete with 16 tiles", got)
	}
}

func TestResumeRejectsAJobThatIsNotFailedOrCancelled(t *testing.T) {
	one := tilecalc.TileBounds{MinX: 330, MaxX: 330, MinY: 400, MaxY: 400, Zoom: 10}
	s := newJobServer(t, slowTables(100*time.Millisecond, slowArea, otherArea))
	done := s.startJob(t, jobBody(one))
	s.finalRecord(t, done.ID)
	running := s.startJob(t, jobBody(slowArea))
	s.waitState(t, running.ID, jobRunning)
	queued := s.startJob(t, jobBody(otherArea))

	tests := []struct {
		name   string
		id     string
		status int
		code   string
	}{
		{"rejects a complete job", done.ID, http.StatusConflict, "invalid_state"},
		{"rejects a running job", running.ID, http.StatusConflict, "invalid_state"},
		{"rejects a queued job", queued.ID, http.StatusConflict, "invalid_state"},
		{"rejects an unknown job", "000000000000", http.StatusNotFound, "job_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkError(t, s.request(t, http.MethodPost, "/api/jobs/"+tt.id+"/resume", ""), tt.status, tt.code)
		})
	}
	checkError(t, s.request(t, http.MethodGet, "/api/jobs/"+done.ID+"/resume", ""), http.StatusMethodNotAllowed, "method_not_allowed")
	s.cancelAndWait(t, queued.ID)
	s.cancelAndWait(t, running.ID)
}

func TestQueueFullReturns429(t *testing.T) {
	s := newJobServer(t, slowTable(slowArea, 200*time.Millisecond))
	running := s.startJob(t, jobBody(slowArea))
	s.waitState(t, running.ID, jobRunning)

	// Each queued job has its own name, so no job conflicts with the output file of another.
	var queued []string
	for i := 1; i <= jobs.MaxQueued; i++ {
		rec := s.startJob(t, namedJobBody(otherArea, fmt.Sprintf("queued-%d", i)))
		queued = append(queued, rec.ID)
	}
	checkError(t, s.request(t, http.MethodPost, "/api/jobs", namedJobBody(otherArea, "over")), http.StatusTooManyRequests, "queue_full")

	for _, id := range queued {
		s.cancelAndWait(t, id)
	}
	s.cancelAndWait(t, running.ID)
}

func TestAQueuedJobChecksIfExistsAgainWhenItStarts(t *testing.T) {
	s := newJobServer(t, slowTables(100*time.Millisecond, slowArea, otherArea))
	running := s.startJob(t, jobBody(slowArea))
	s.waitState(t, running.ID, jobRunning)
	queued := s.startJob(t, jobBody(otherArea))

	// The file appears while the job waits in the queue, as the file of another job can.
	if err := os.WriteFile(filepath.Join(s.handler.outputDir, queued.OutputFile), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.cancelAndWait(t, running.ID)

	final := s.finalRecord(t, queued.ID)
	if final.State != jobFailed || !strings.Contains(final.Error, "Output file exists") {
		t.Errorf("final record = %+v, want failed with the output_exists message", final)
	}
	if n := s.areaRequests(otherArea); n != 0 {
		t.Errorf("the job sent %d tile requests, want 0", n)
	}
	data, err := os.ReadFile(filepath.Join(s.handler.outputDir, queued.OutputFile))
	if err != nil || string(data) != "other" {
		t.Errorf("the existing file = %q, %v, want it kept", data, err)
	}
}

func TestAQueuedOverwriteJobKeepsTheFileUntilItStarts(t *testing.T) {
	s := newJobServer(t, slowTable(slowArea, 100*time.Millisecond))
	running := s.startJob(t, jobBody(slowArea))
	s.waitState(t, running.ID, jobRunning)

	// The queued job names the output file of the running job, so a delete at submit would remove a file in use.
	queued := s.startJob(t, strings.TrimSuffix(jobBody(slowArea), "}")+`,"if_exists":"overwrite"}`)
	if _, err := os.Stat(filepath.Join(s.handler.outputDir, running.OutputFile)); err != nil {
		t.Errorf("output file of the running job after the submit: %v, want it kept", err)
	}
	s.cancelAndWait(t, queued.ID)
	s.cancelAndWait(t, running.ID)
}
