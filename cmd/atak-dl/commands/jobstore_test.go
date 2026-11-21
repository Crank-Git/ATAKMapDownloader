package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// storedRequest returns the stored job request of a job for the tiles of tb on the map source "Test Tiles".
func storedRequest(tb tilecalc.TileBounds) jobRequest {
	b := boundsOf(tb)
	return jobRequest{Source: "Test Tiles", Bounds: b[:], MinZoom: tb.Zoom, MaxZoom: tb.Zoom, OutputFormat: output.MBTiles}
}

// writeJobsFile writes jobs as the jobs.jsonl file of a new data directory and returns the directory.
func writeJobsFile(t *testing.T, jobs ...storedJob) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for _, j := range jobs {
		line, err := json.Marshal(j)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// readJobsFile returns the bytes of the jobs.jsonl file of the data directory dir.
func readJobsFile(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRestartFailsRunningJobsAndRunsQueuedJobs(t *testing.T) {
	created := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	running := storedJob{
		jobRecord: jobRecord{ID: "aaaaaaaaaaaa", Source: "Test Tiles", State: jobRunning, OutputFile: "was_running.mbtiles", CreatedAt: created},
		Request:   storedRequest(slowArea),
	}
	queued := storedJob{
		jobRecord: jobRecord{ID: "bbbbbbbbbbbb", Source: "Test Tiles", State: jobQueued, OutputFile: "was_queued.mbtiles", Label: "night shift", CreatedAt: created.Add(time.Second)},
		Request:   storedRequest(otherArea),
	}
	complete := storedJob{
		jobRecord: jobRecord{ID: "cccccccccccc", Source: "Test Tiles", State: jobComplete, OutputFile: "was_complete.mbtiles", CreatedAt: created.Add(-time.Hour)},
		Request:   storedRequest(fourTiles),
	}
	dir := writeJobsFile(t, running, queued, complete)
	s := newDataDirJobServer(t, nil, "Test Tiles", "png", dir)

	got := s.getRecord(t, running.ID)
	if got.State != jobFailed || got.Reason != "server_restarted" {
		t.Errorf("record of the running job = state %q, reason %q, want failed with reason server_restarted", got.State, got.Reason)
	}
	if got.FinishedAt == nil {
		t.Error("record of the running job has no finished_at")
	}

	final := s.finalRecord(t, queued.ID)
	if final.State != jobComplete || final.Label != "night shift" || final.OutputFile != "was_queued.mbtiles" {
		t.Errorf("final record of the queued job = %+v, want complete with its label and output file", final)
	}
	if final.StartedAt == nil || final.FinishedAt == nil {
		t.Errorf("final record of the queued job = started_at %v, finished_at %v, want both", final.StartedAt, final.FinishedAt)
	}
	if n := s.areaRequests(otherArea); n != 4 {
		t.Errorf("the queued job sent %d tile requests, want 4", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "was_queued.mbtiles")); err != nil {
		t.Errorf("output file of the queued job: %v", err)
	}
	if n := s.areaRequests(slowArea); n != 0 {
		t.Errorf("the failed job sent %d tile requests, want 0", n)
	}

	if got := s.getRecord(t, complete.ID); got.State != jobComplete {
		t.Errorf("record of the complete job = state %q, want complete", got.State)
	}

	// The next start reads the failed state, so the store holds it.
	reopened := newDataDirHandler(t, map[string]string{}, dir)
	if got := reopened.jobs[running.ID].snapshot(); got.State != jobFailed || got.Reason != "server_restarted" {
		t.Errorf("record of the running job after a second restart = state %q, reason %q, want failed with server_restarted", got.State, got.Reason)
	}
}

func TestARestartedFailedJobResumes(t *testing.T) {
	running := storedJob{
		jobRecord: jobRecord{ID: "aaaaaaaaaaaa", Source: "Test Tiles", State: jobRunning, OutputFile: "was_running.mbtiles", CreatedAt: time.Now().UTC()},
		Request:   storedRequest(fourTiles),
	}
	s := newDataDirJobServer(t, nil, "Test Tiles", "png", writeJobsFile(t, running))
	resp := s.request(t, http.MethodPost, "/api/jobs/"+running.ID+"/resume", "")
	closeBody(resp)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST resume status = %d, want 202", resp.StatusCode)
	}
	final := s.finalRecord(t, running.ID)
	if final.State != jobComplete || final.Reason != "" {
		t.Errorf("final record = %+v, want complete with no reason", final)
	}
}

func TestEveryStateChangeAppendsALine(t *testing.T) {
	s := newJobServer(t, nil)
	rec := s.startJob(t, jobBody(fourTiles))
	s.finalRecord(t, rec.ID)

	var states []jobState
	for line := range strings.SplitSeq(strings.TrimSpace(readJobsFile(t, s.handler.outputDir)), "\n") {
		var stored storedJob
		if err := json.Unmarshal([]byte(line), &stored); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		if stored.ID != rec.ID || stored.Request.Source != "Test Tiles" {
			t.Errorf("line = %q, want the job and its request", line)
		}
		states = append(states, stored.State)
	}
	want := []jobState{jobQueued, jobRunning, jobComplete}
	if fmt.Sprint(states) != fmt.Sprint(want) {
		t.Errorf("states of the lines = %v, want %v", states, want)
	}
}

func TestListJobsPagesNewestFirst(t *testing.T) {
	created := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var stored []storedJob
	for i := range 120 {
		stored = append(stored, storedJob{
			jobRecord: jobRecord{ID: fmt.Sprintf("%012d", i), Source: "Test Tiles", State: jobComplete,
				OutputFile: fmt.Sprintf("job%d.mbtiles", i), CreatedAt: created.Add(time.Duration(i) * time.Minute)},
			Request: storedRequest(fourTiles),
		})
	}
	s := newDataDirJobServer(t, nil, "Test Tiles", "png", writeJobsFile(t, stored...))

	tests := []struct {
		query   string
		page    int
		count   int
		firstID string
	}{
		{"", 1, 50, fmt.Sprintf("%012d", 119)},
		{"?page=1", 1, 50, fmt.Sprintf("%012d", 119)},
		{"?page=2", 2, 50, fmt.Sprintf("%012d", 69)},
		{"?page=3", 3, 20, fmt.Sprintf("%012d", 19)},
		{"?page=4", 4, 0, ""},
	}
	for _, tt := range tests {
		t.Run("GET /api/jobs"+tt.query, func(t *testing.T) {
			resp := s.request(t, http.MethodGet, "/api/jobs"+tt.query, "")
			defer closeBody(resp)
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("status = %d, Content-Type %q, want 200 application/json", resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			var list jobList
			if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
				t.Fatal(err)
			}
			if list.Page != tt.page || list.Pages != 3 || len(list.Jobs) != tt.count {
				t.Fatalf("page %d of %d with %d jobs, want page %d of 3 with %d jobs", list.Page, list.Pages, len(list.Jobs), tt.page, tt.count)
			}
			if list.Jobs == nil {
				t.Error("jobs is null, want an array")
			}
			if tt.count > 0 && list.Jobs[0].ID != tt.firstID {
				t.Errorf("first job = %s, want %s", list.Jobs[0].ID, tt.firstID)
			}
			for i := 1; i < len(list.Jobs); i++ {
				if list.Jobs[i].CreatedAt.After(list.Jobs[i-1].CreatedAt) {
					t.Fatalf("job %d is newer than job %d, want newest first", i, i-1)
				}
			}
		})
	}
}

func TestListJobsRejectsAPageThatIsNotAWholeNumberOf1OrMore(t *testing.T) {
	s := newJobServer(t, nil)
	for _, page := range []string{"0", "-1", "x", "1.5", ""} {
		t.Run("page="+page, func(t *testing.T) {
			checkError(t, s.request(t, http.MethodGet, "/api/jobs?page="+page, ""), http.StatusBadRequest, "invalid_query")
		})
	}
}

func TestListJobsHoldsAQueuedJobAndANewJob(t *testing.T) {
	s := newJobServer(t, slowTables(100*time.Millisecond, slowArea))
	running := s.startJob(t, jobBody(slowArea))
	s.waitState(t, running.ID, jobRunning)
	queued := s.startJob(t, jobBody(otherArea))

	resp := s.request(t, http.MethodGet, "/api/jobs", "")
	var list jobList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	closeBody(resp)
	if len(list.Jobs) != 2 || list.Jobs[0].ID != queued.ID || list.Jobs[0].Position != 1 || list.Jobs[1].State != jobRunning {
		t.Errorf("jobs = %+v, want the queued job at position 1 and then the running job", list.Jobs)
	}
	s.cancelAndWait(t, queued.ID)
	s.cancelAndWait(t, running.ID)
}

func TestJobLabelHas1To80Characters(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		status int
		label  string
	}{
		{"accepts a job with no label", "", http.StatusAccepted, ""},
		{"rejects an empty label", `,"label":""`, http.StatusBadRequest, ""},
		{"accepts a label of 1 character", `,"label":"a"`, http.StatusAccepted, "a"},
		{"accepts a label of 80 characters", `,"label":"` + strings.Repeat("a", 80) + `"`, http.StatusAccepted, strings.Repeat("a", 80)},
		{"accepts a label of 80 characters of 2 bytes", `,"label":"` + strings.Repeat("é", 80) + `"`, http.StatusAccepted, strings.Repeat("é", 80)},
		{"rejects a label of 81 characters", `,"label":"` + strings.Repeat("a", 81) + `"`, http.StatusBadRequest, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newJobServer(t, nil)
			body := strings.TrimSuffix(jobBody(fourTiles), "}") + tt.field + "}"
			resp := s.request(t, http.MethodPost, "/api/jobs", body)
			if tt.status != http.StatusAccepted {
				checkError(t, resp, tt.status, "invalid_label")
				return
			}
			defer closeBody(resp)
			if resp.StatusCode != http.StatusAccepted {
				raw, _ := io.ReadAll(resp.Body) // The body is only for the failure message.
				t.Fatalf("status = %d, want 202, body %s", resp.StatusCode, raw)
			}
			rec := decodeRecord(t, resp.Body)
			if rec.Label != tt.label {
				t.Errorf("label = %q, want %q", rec.Label, tt.label)
			}
			s.finalRecord(t, rec.ID)
		})
	}
}

func TestSameOutputFileNameGetsSuffix(t *testing.T) {
	s := newJobServer(t, slowTables(100*time.Millisecond, slowArea))
	running := s.startJob(t, jobBody(slowArea))
	s.waitState(t, running.ID, jobRunning)
	second := s.startJob(t, jobBody(slowArea))
	third := s.startJob(t, jobBody(slowArea))

	ext := filepath.Ext(running.OutputFile)
	base := strings.TrimSuffix(running.OutputFile, ext)
	if want := base + "-2" + ext; second.OutputFile != want {
		t.Errorf("output file of the second job = %q, want %q", second.OutputFile, want)
	}
	if want := base + "-3" + ext; third.OutputFile != want {
		t.Errorf("output file of the third job = %q, want %q", third.OutputFile, want)
	}
	for _, id := range []string{third.ID, second.ID, running.ID} {
		s.cancelAndWait(t, id)
	}
}

func TestSameDataPackageNameGetsSuffix(t *testing.T) {
	s := newJobServer(t, slowTables(100*time.Millisecond, slowArea))
	running := s.startJob(t, jobBody(slowArea))
	s.waitState(t, running.ID, jobRunning)
	// Both queued jobs name one data package, and their output file names differ by area.
	first := s.startJob(t, dataPackageJob(""))
	second := s.startJob(t, dataPackageJob(`,"name":"other"`))
	s.cancelAndWait(t, running.ID)

	tests := []struct {
		name string
		id   string
		zip  string
	}{
		{"keeps the package name of the first job", first.ID, "Mt_Hood_SAR.zip"},
		{"adds -2 to the package name of the second job", second.ID, "Mt_Hood_SAR-2.zip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			final := s.finalRecord(t, tt.id)
			if final.State != jobComplete || final.OutputFile != tt.zip {
				t.Errorf("final record = state %q, output file %q, want complete with %q", final.State, final.OutputFile, tt.zip)
			}
		})
	}
}

func TestJobsFileHoldsNoToken(t *testing.T) {
	s := newJobServer(t, nil)
	s.handler.auth = newTokenAuth(testToken)
	srv := httptest.NewServer(newServerHandler(s.handler))
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/jobs", strings.NewReader(jobBody(fourTiles)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	rec := decodeRecord(t, resp.Body)
	closeBody(resp)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /api/jobs status = %d, want 202", resp.StatusCode)
	}
	s.finalRecord(t, rec.ID)

	data := readJobsFile(t, s.handler.outputDir)
	if !strings.Contains(data, rec.ID) {
		t.Fatalf("jobs.jsonl = %q, want the lines of the job", data)
	}
	if strings.Contains(data, testToken) {
		t.Errorf("jobs.jsonl holds the token: %q", data)
	}
}
