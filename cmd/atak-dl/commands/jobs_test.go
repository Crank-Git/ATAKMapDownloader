package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// sseEvent is one event that the test client reads from an SSE stream.
type sseEvent struct {
	name string
	data string
}

// jobServer is a server with a job API and a test tile server behind its map source.
type jobServer struct {
	handler *WebHandler
	url     string
}

// newJobServer starts a server whose one map source "Test Tiles" points at a tile server with table.
func newJobServer(t *testing.T, table testtiles.Table) *jobServer {
	t.Helper()
	tiles := testtiles.New(t, table)
	xml := fmt.Sprintf(`<?xml version="1.0"?>
<customMapSource>
    <name>Test Tiles</name>
    <minZoom>0</minZoom>
    <maxZoom>18</maxZoom>
    <tileType>png</tileType>
    <url>%s</url>
</customMapSource>`, tiles.URLTemplate())
	h := newTestHandler(t, map[string]string{"tiles.xml": xml})
	h.outputDir = t.TempDir()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &jobServer{handler: h, url: srv.URL}
}

// boundsOf returns [w,s,e,n] bounds that hold exactly the tiles of tb.
func boundsOf(tb tilecalc.TileBounds) [4]float64 {
	edges := tilecalc.NewTileCalculator().TileBoundsToGeographic(&tb)
	// The tile edges belong to the next tile, so the bounds move inside them.
	const inset = 1e-6
	return [4]float64{edges.MinLon + inset, edges.MinLat + inset, edges.MaxLon - inset, edges.MaxLat - inset}
}

// jobBody returns a POST /api/jobs body for the tiles of tb.
func jobBody(tb tilecalc.TileBounds) string {
	b := boundsOf(tb)
	// TODO(#25): Remove the concurrency of 1 when the engine updates progress with atomic operations only.
	// Every tile goroutine writes the shared Progress fields, so a concurrent job fails under -race.
	return fmt.Sprintf(`{"source":"Test Tiles","bounds":[%f,%f,%f,%f],"min_zoom":%d,"max_zoom":%d,"concurrency":1}`,
		b[0], b[1], b[2], b[3], tb.Zoom, tb.Zoom)
}

// slowTable returns a table that delays every tile of tb by delay.
func slowTable(tb tilecalc.TileBounds, delay time.Duration) testtiles.Table {
	table := testtiles.Table{}
	for x := tb.MinX; x <= tb.MaxX; x++ {
		for y := tb.MinY; y <= tb.MaxY; y++ {
			table[testtiles.Tile{Z: tb.Zoom, X: x, Y: y}] = testtiles.Response{Kind: testtiles.PNG, Delay: delay}
		}
	}
	return table
}

// request sends one request to the server and returns the response.
func (s *jobServer) request(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, s.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// startJob posts body and returns the job record of the 202 response.
func (s *jobServer) startJob(t *testing.T, body string) jobRecord {
	t.Helper()
	resp := s.request(t, http.MethodPost, "/api/jobs", body)
	defer closeBody(resp)
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(resp.Body) // The body is only for the failure message.
		t.Fatalf("POST /api/jobs status = %d, want 202, body %s", resp.StatusCode, raw)
	}
	return decodeRecord(t, resp.Body)
}

// openEvents opens the SSE stream of job id.
func (s *jobServer) openEvents(t *testing.T, id string) (*http.Response, *bufio.Reader) {
	t.Helper()
	resp := s.request(t, http.MethodGet, "/api/jobs/"+id+"/events", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET events status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return resp, bufio.NewReader(resp.Body)
}

// finalRecord reads the SSE stream of job id to its end and returns the record of its last state event.
func (s *jobServer) finalRecord(t *testing.T, id string) jobRecord {
	t.Helper()
	resp, r := s.openEvents(t, id)
	defer closeBody(resp)
	events := readAllEvents(t, r)
	last := events[len(events)-1]
	if last.name != "state" {
		t.Fatalf("last event = %q, want state", last.name)
	}
	return decodeRecord(t, strings.NewReader(last.data))
}

func closeBody(resp *http.Response) {
	_ = resp.Body.Close() // The test read what it needs, so a close error loses nothing.
}

func decodeRecord(t *testing.T, r io.Reader) jobRecord {
	t.Helper()
	var rec jobRecord
	if err := json.NewDecoder(r).Decode(&rec); err != nil {
		t.Fatalf("decode the job record: %v", err)
	}
	return rec
}

// readEvent returns the next event of an SSE stream, or false at the end of the stream.
func readEvent(t *testing.T, r *bufio.Reader) (sseEvent, bool) {
	t.Helper()
	var e sseEvent
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" && e.name == "" {
				return e, false
			}
			t.Fatalf("read the event stream: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			return e, true
		case strings.HasPrefix(line, "event: "):
			e.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			e.data = strings.TrimPrefix(line, "data: ")
		default:
			t.Fatalf("unexpected stream line %q", line)
		}
	}
}

func readAllEvents(t *testing.T, r *bufio.Reader) []sseEvent {
	t.Helper()
	var events []sseEvent
	for {
		e, ok := readEvent(t, r)
		if !ok {
			return events
		}
		events = append(events, e)
	}
}

func TestJobAPIStreamsEveryEventOfA1000TileJob(t *testing.T) {
	area := tilecalc.TileBounds{MinX: 300, MaxX: 339, MinY: 400, MaxY: 424, Zoom: 10}
	s := newJobServer(t, nil)

	rec := s.startJob(t, jobBody(area))
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(rec.ID) {
		t.Errorf("id = %q, want 12 lowercase hex characters", rec.ID)
	}
	if rec.State != jobRunning || rec.Total != 1000 {
		t.Errorf("record = %+v, want state running and total 1000", rec)
	}

	start := time.Now()
	resp, r := s.openEvents(t, rec.ID)
	defer closeBody(resp)
	events := readAllEvents(t, r)
	elapsed := time.Since(start)

	if len(events) < 2 || events[0].name != "state" {
		t.Fatalf("events = %v, want a state event first and a final state event", events)
	}
	progressCount := checkProgress(t, events[1:len(events)-1], 1000)
	// One more event than 4 per second allows for a tick at each end of the stream.
	if limit := int(elapsed.Seconds()*4) + 1; progressCount > limit {
		t.Errorf("%d progress events in %v, want at most %d", progressCount, elapsed, limit)
	}

	final := decodeRecord(t, strings.NewReader(events[len(events)-1].data))
	if final.State != jobComplete || final.Downloaded != 1000 || final.Failed != 0 || final.Error != "" {
		t.Errorf("final record = %+v, want complete with 1000 tiles", final)
	}

	got := s.getRecord(t, rec.ID)
	if got.State != jobComplete || got.Downloaded != 1000 {
		t.Errorf("GET record = %+v, want complete with 1000 tiles", got)
	}
	if _, err := os.Stat(filepath.Join(s.handler.outputDir, got.OutputFile)); err != nil {
		t.Errorf("output file: %v", err)
	}
}

// checkProgress fails the test when an event is not a progress event for total tiles,
// or when the downloaded count decreases. It returns the number of events.
func checkProgress(t *testing.T, events []sseEvent, total int) int {
	t.Helper()
	var lastDownloaded int64
	for _, e := range events {
		if e.name != "progress" {
			t.Fatalf("event %q between the first and the last event, want progress", e.name)
		}
		var p progressEvent
		if err := json.Unmarshal([]byte(e.data), &p); err != nil {
			t.Fatalf("decode progress %q: %v", e.data, err)
		}
		if p.Total != total || p.Downloaded < lastDownloaded {
			t.Errorf("progress %+v after downloaded %d, want total %d and no decrease", p, lastDownloaded, total)
		}
		lastDownloaded = p.Downloaded
	}
	return len(events)
}

// getRecord returns the record of GET /api/jobs/{id}.
func (s *jobServer) getRecord(t *testing.T, id string) jobRecord {
	t.Helper()
	resp := s.request(t, http.MethodGet, "/api/jobs/"+id, "")
	defer closeBody(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/jobs/%s status = %d, want 200", id, resp.StatusCode)
	}
	return decodeRecord(t, resp.Body)
}

// slowArea is a 4×4 tile area. With 100 ms per tile, its job runs for about 1.6 s.
var slowArea = tilecalc.TileBounds{MinX: 300, MaxX: 303, MinY: 400, MaxY: 403, Zoom: 10}

func TestJobEventsSendTheStateFirstAfterAReconnect(t *testing.T) {
	s := newJobServer(t, slowTable(slowArea, 100*time.Millisecond))
	rec := s.startJob(t, jobBody(slowArea))

	resp, r := s.openEvents(t, rec.ID)
	if e, _ := readEvent(t, r); e.name != "state" {
		t.Fatalf("first event = %q, want state", e.name)
	}
	if e, _ := readEvent(t, r); e.name != "progress" {
		t.Fatalf("second event = %q, want progress", e.name)
	}
	closeBody(resp)

	resp, r = s.openEvents(t, rec.ID)
	e, _ := readEvent(t, r)
	closeBody(resp)
	if e.name != "state" {
		t.Fatalf("first event after the reconnect = %q, want state", e.name)
	}
	if got := decodeRecord(t, strings.NewReader(e.data)); got.State != jobRunning || got.ID != rec.ID {
		t.Errorf("state after the reconnect = %+v, want the running job", got)
	}

	// The job continues after the client leaves, and it ends complete.
	if final := s.finalRecord(t, rec.ID); final.State != jobComplete || final.Downloaded != 16 {
		t.Errorf("final record = %+v, want complete with 16 tiles", final)
	}
}

func TestJobAPIRejectsASecondJobWhileOneRuns(t *testing.T) {
	s := newJobServer(t, slowTable(slowArea, 100*time.Millisecond))
	rec := s.startJob(t, jobBody(slowArea))

	resp := s.request(t, http.MethodPost, "/api/jobs", jobBody(slowArea))
	checkError(t, resp, http.StatusConflict, "job_running")

	closeBody(s.request(t, http.MethodDelete, "/api/jobs/"+rec.ID, ""))
	s.finalRecord(t, rec.ID)
	// The job lock is free again after the job ends.
	next := s.startJob(t, jobBody(slowArea))
	closeBody(s.request(t, http.MethodDelete, "/api/jobs/"+next.ID, ""))
	s.finalRecord(t, next.ID)
}

func TestDeleteCancelsARunningJob(t *testing.T) {
	s := newJobServer(t, slowTable(slowArea, 100*time.Millisecond))
	rec := s.startJob(t, jobBody(slowArea))
	stream, r := s.openEvents(t, rec.ID)
	defer closeBody(stream)
	// A progress event shows that the engine runs, so the cancel stops a job in progress.
	for _, want := range []string{"state", "progress"} {
		if e, _ := readEvent(t, r); e.name != want {
			t.Fatalf("event = %q, want %s", e.name, want)
		}
	}

	start := time.Now()
	resp := s.request(t, http.MethodDelete, "/api/jobs/"+rec.ID, "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("DELETE status = %d, want 202", resp.StatusCode)
	}
	if got := decodeRecord(t, resp.Body); got.ID != rec.ID {
		t.Errorf("DELETE record id = %q, want %q", got.ID, rec.ID)
	}
	closeBody(resp)

	events := readAllEvents(t, r)
	last := events[len(events)-1]
	if last.name != "state" {
		t.Fatalf("last event = %q, want state", last.name)
	}
	if final := decodeRecord(t, strings.NewReader(last.data)); final.State != jobCancelled {
		t.Errorf("final state = %q, want cancelled", final.State)
	}
	// One tile takes 100 ms, so a job that stops after the current tile ends well within 1 s.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("the job ended %v after the cancel, want less than 1 s", elapsed)
	}
}

func TestAFailedJobRecordsTheEngineError(t *testing.T) {
	area := tilecalc.TileBounds{MinX: 300, MaxX: 301, MinY: 400, MaxY: 401, Zoom: 10}
	table := testtiles.Table{}
	for x := area.MinX; x <= area.MaxX; x++ {
		for y := area.MinY; y <= area.MaxY; y++ {
			table[testtiles.Tile{Z: area.Zoom, X: x, Y: y}] = testtiles.Response{Kind: testtiles.NotFound}
		}
	}
	s := newJobServer(t, table)
	rec := s.startJob(t, jobBody(area))

	resp, r := s.openEvents(t, rec.ID)
	defer closeBody(resp)
	events := readAllEvents(t, r)
	var logged bool
	for _, e := range events {
		if e.name == "log" {
			var l logEvent
			if err := json.Unmarshal([]byte(e.data), &l); err != nil {
				t.Fatalf("decode log %q: %v", e.data, err)
			}
			logged = l.Level == "error" && l.Message != ""
		}
	}
	if !logged {
		t.Errorf("events = %v, want a log event with level error", events)
	}
	final := decodeRecord(t, strings.NewReader(events[len(events)-1].data))
	if final.State != jobFailed || final.Failed != 4 || !strings.Contains(final.Error, "failure rate") {
		t.Errorf("final record = %+v, want failed with 4 failed tiles and the engine error", final)
	}
	if got := s.getRecord(t, rec.ID); got.Error != final.Error {
		t.Errorf("GET error = %q, want %q", got.Error, final.Error)
	}
}

// checkError fails the test when resp does not carry status and an error body with code.
func checkError(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	defer closeBody(resp)
	if resp.StatusCode != status {
		t.Errorf("status = %d, want %d", resp.StatusCode, status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body apiError
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("body does not parse as the error shape: %v", err)
	}
	if body.Error.Code != code || body.Error.Message == "" {
		t.Errorf("error = %+v, want code %q and a message", body.Error, code)
	}
}

func TestJobAPIErrorResponses(t *testing.T) {
	s := newJobServer(t, nil)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{"get rejects an unknown job", http.MethodGet, "/api/jobs/000000000000", "", http.StatusNotFound, "job_not_found"},
		{"events rejects an unknown job", http.MethodGet, "/api/jobs/000000000000/events", "", http.StatusNotFound, "job_not_found"},
		{"delete rejects an unknown job", http.MethodDelete, "/api/jobs/000000000000", "", http.StatusNotFound, "job_not_found"},
		{"jobs rejects GET", http.MethodGet, "/api/jobs", "", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"job rejects POST", http.MethodPost, "/api/jobs/000000000000", "", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"jobs rejects an unknown route", http.MethodGet, "/api/jobs/a/b", "", http.StatusNotFound, "not_found"},
		{"post rejects a body that is not JSON", http.MethodPost, "/api/jobs", "{", http.StatusBadRequest, "invalid_json"},
		{"post rejects a missing source name", http.MethodPost, "/api/jobs", `{"bounds":[0,0,1,1]}`, http.StatusBadRequest, "unknown_source"},
		{"post rejects an unknown source", http.MethodPost, "/api/jobs", `{"source":"Nope","bounds":[0,0,1,1]}`, http.StatusNotFound, "unknown_source"},
		{"post rejects three bounds values", http.MethodPost, "/api/jobs", `{"source":"Test Tiles","bounds":[0,0,1]}`, http.StatusBadRequest, "invalid_bounds"},
		{"post rejects inverted bounds", http.MethodPost, "/api/jobs", `{"source":"Test Tiles","bounds":[0,10,1,0]}`, http.StatusBadRequest, "invalid_bounds"},
		{"post rejects min_zoom above max_zoom", http.MethodPost, "/api/jobs", `{"source":"Test Tiles","bounds":[0,0,1,1],"min_zoom":5,"max_zoom":4}`, http.StatusBadRequest, "zoom_out_of_range"},
		{"post rejects a negative min_zoom", http.MethodPost, "/api/jobs", `{"source":"Test Tiles","bounds":[0,0,1,1],"min_zoom":-1,"max_zoom":4}`, http.StatusBadRequest, "zoom_out_of_range"},
		{"post rejects a concurrency of 0", http.MethodPost, "/api/jobs", `{"source":"Test Tiles","bounds":[0,0,1,1],"max_zoom":1,"concurrency":0}`, http.StatusBadRequest, "invalid_concurrency"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkError(t, s.request(t, tt.method, tt.path, tt.body), tt.status, tt.code)
		})
	}
}
