package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

// jobRequest is the body of POST /api/jobs, with the schema of docs/specs/features/05-security-hardening.md.
// A nil pointer field means that the client did not send the setting, so the engine default applies.
type jobRequest struct {
	Source           string    `json:"source"`
	Bounds           []float64 `json:"bounds"`
	MinZoom          int       `json:"min_zoom"`
	MaxZoom          int       `json:"max_zoom"`
	Name             string    `json:"name"`
	Concurrency      *int      `json:"concurrency"`
	FailureTolerance *float64  `json:"failure_tolerance"`
	IgnoreErrors     *bool     `json:"ignore_errors"`
	VerifyTLS        *bool     `json:"verify_tls"`
}

// engineConfig returns the engine configuration for the request.
func (req jobRequest) engineConfig() download.Config {
	cfg := download.DefaultConfig()
	if req.Concurrency != nil {
		cfg.MaxConcurrent = *req.Concurrency
	}
	if req.FailureTolerance != nil {
		cfg.MaxFailureRate = *req.FailureTolerance
	}
	if req.IgnoreErrors != nil {
		cfg.IgnoreErrors = *req.IgnoreErrors
	}
	if req.VerifyTLS != nil {
		cfg.VerifyTLS = *req.VerifyTLS
	}
	return cfg
}

// jobState is the state of a job. Feature 12 adds "queued".
type jobState string

const (
	jobRunning   jobState = "running"
	jobComplete  jobState = "complete"
	jobFailed    jobState = "failed"
	jobCancelled jobState = "cancelled"
)

// jobRecord is the job record that the job API returns and that each state event carries.
type jobRecord struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Bounds     []float64 `json:"bounds"`
	MinZoom    int       `json:"min_zoom"`
	MaxZoom    int       `json:"max_zoom"`
	State      jobState  `json:"state"`
	OutputFile string    `json:"output_file"`
	Total      int       `json:"total"`
	Downloaded int64     `json:"downloaded"`
	// Skipped stays 0 until feature 07 adds resume, because only resume skips a tile.
	Skipped   int64     `json:"skipped"`
	Failed    int64     `json:"failed"`
	CreatedAt time.Time `json:"created_at"`
	Error     string    `json:"error"`
}

// progressEvent is the data of a progress event.
type progressEvent struct {
	Downloaded int64   `json:"downloaded"`
	Skipped    int64   `json:"skipped"`
	Failed     int64   `json:"failed"`
	Total      int     `json:"total"`
	Rate       float64 `json:"rate"`
	ETASeconds float64 `json:"eta_seconds"`
}

// logEvent is the data of a log event.
type logEvent struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// sseMessage is one SSE event: its name and the value that its data line encodes.
type sseMessage struct {
	name string
	data any
}

// subscriber is one open SSE stream of a job.
type subscriber struct {
	// events carries the events from the event goroutine. The event goroutine closes it after the final state.
	events chan sseMessage
	// gone closes when the stream handler returns, so the event goroutine never blocks on a closed stream.
	gone chan struct{}
}

// jobResult is the outcome of the engine run of a job.
type jobResult struct {
	state jobState
	err   error
}

// job is the in-memory state of one job.
// Its event goroutine is the only sender of its events (FR-security-hardening-13).
type job struct {
	id     string
	cancel context.CancelFunc

	// progress carries the engine progress to the event goroutine.
	progress chan progressEvent
	// result carries the outcome after the engine returns, and after its last progress send.
	result    chan jobResult
	subscribe chan *subscriber
	// done closes after the event goroutine sends the final state event.
	done chan struct{}

	mu     sync.Mutex
	record jobRecord
}

// progressInterval gives at most 4 progress events per second per job (FR-security-hardening-14).
const progressInterval = time.Second / 4

// snapshot returns a copy of the job record.
func (j *job) snapshot() jobRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.record
}

// sendEvents sends the events of the job to its subscribers until the job ends.
func (j *job) sendEvents() {
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()
	subs := map[*subscriber]struct{}{}
	var latest progressEvent
	pending := false
	for {
		select {
		case p := <-j.progress:
			latest, pending = p, true
		case <-ticker.C:
			if !pending {
				continue
			}
			pending = false
			j.applyProgress(latest)
			for s := range subs {
				offer(subs, s, sseMessage{"progress", latest})
			}
		case s := <-j.subscribe:
			subs[s] = struct{}{}
			// A new subscriber has an empty buffer, so this send does not block.
			s.events <- sseMessage{"state", j.snapshot()}
		case res := <-j.result:
			if pending {
				j.applyProgress(latest)
			}
			j.finish(subs, res)
			return
		}
	}
}

// applyProgress copies the counts of p into the job record.
func (j *job) applyProgress(p progressEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.record.Downloaded, j.record.Failed = p.Downloaded, p.Failed
}

// offer sends m to s and drops m when the buffer of s is full.
// The next progress event carries the counts again, so a dropped one loses no count.
func offer(subs map[*subscriber]struct{}, s *subscriber, m sseMessage) {
	select {
	case s.events <- m:
	case <-s.gone:
		delete(subs, s)
	default:
	}
}

// finish records the result, sends the final state event to every subscriber and ends their streams.
func (j *job) finish(subs map[*subscriber]struct{}, res jobResult) {
	// The engine finished its sends before the result, so the buffer holds the last counts.
	for drained := false; !drained; {
		select {
		case p := <-j.progress:
			j.applyProgress(p)
		default:
			drained = true
		}
	}
	j.mu.Lock()
	j.record.State = res.state
	if res.state == jobFailed {
		j.record.Error = res.err.Error()
	}
	final := j.record
	j.mu.Unlock()

	for s := range subs {
		if res.state == jobFailed {
			deliver(s, sseMessage{"log", logEvent{Level: "error", Message: final.Error}})
		}
		deliver(s, sseMessage{"state", final})
		close(s.events)
	}
	close(j.done)
}

// deliver sends m to s and waits until s takes it or its stream ends.
func deliver(s *subscriber, m sseMessage) {
	select {
	case s.events <- m:
	case <-s.gone:
	}
}

// newJobID returns 12 lowercase hex characters from crypto/rand.
func newJobID() string {
	b := make([]byte, 6)
	// rand.Read never returns an error. It ends the program when the system source fails.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// routeJobs registers the job API routes on mux.
// The routes without a method catch the requests that no route with a method matches,
// so every error response has the error shape.
func (h *WebHandler) routeJobs(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/jobs", h.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", h.getJob)
	mux.HandleFunc("GET /api/jobs/{id}/events", h.streamJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", h.cancelJob)
	notAllowed := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
	mux.HandleFunc("/api/jobs", notAllowed)
	mux.HandleFunc("/api/jobs/{id}", notAllowed)
	mux.HandleFunc("/api/jobs/{id}/events", notAllowed)
	mux.HandleFunc("/api/jobs/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Route not found")
	})
}

// createJob starts a job and returns 202 with its record (FR-security-hardening-8).
// It returns 409 job_running when a job runs (FR-security-hardening-12).
func (h *WebHandler) createJob(w http.ResponseWriter, r *http.Request) {
	var req jobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body is not valid JSON")
		return
	}
	source, bounds, ok := h.checkJobRequest(w, req)
	if !ok {
		return
	}

	h.dlState.Lock()
	if h.dlState.downloading {
		h.dlState.Unlock()
		writeError(w, http.StatusConflict, "job_running", "A job is already running")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := h.newJob(req, bounds, cancel)
	h.dlState.downloading = true
	h.dlState.cancel = cancel
	h.dlState.job = j
	h.dlState.Unlock()

	go j.sendEvents()
	go h.runJob(ctx, j, req, source, bounds)
	writeAccepted(w, j.snapshot())
}

// checkJobRequest returns the map source and the bounds of req.
// It writes the error response and returns false when req is not valid. #17 adds the remaining checks.
func (h *WebHandler) checkJobRequest(w http.ResponseWriter, req jobRequest) (*xmlparser.MapSource, *tilecalc.GeographicBounds, bool) {
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "unknown_source", "Source name is required")
		return nil, nil, false
	}
	if len(req.Bounds) != 4 {
		writeError(w, http.StatusBadRequest, "invalid_bounds", "Bounds must hold four numbers: west, south, east, north")
		return nil, nil, false
	}
	source := h.parser.GetSource(req.Source)
	if source == nil {
		writeError(w, http.StatusNotFound, "unknown_source", "Source not found")
		return nil, nil, false
	}
	bounds, err := tilecalc.NewGeographicBounds(req.Bounds[1], req.Bounds[3], req.Bounds[0], req.Bounds[2])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_bounds", err.Error())
		return nil, nil, false
	}
	// The engine needs at least one zoom level, and a negative zoom level has no tile grid.
	if req.MinZoom < 0 || req.MinZoom > req.MaxZoom {
		writeError(w, http.StatusBadRequest, "zoom_out_of_range", "min_zoom must be 0 or more and not above max_zoom")
		return nil, nil, false
	}
	// The engine semaphore holds concurrency slots, so 0 slots stop the job forever.
	if req.Concurrency != nil && *req.Concurrency < 1 {
		writeError(w, http.StatusBadRequest, "invalid_concurrency", "concurrency must be 1 or more")
		return nil, nil, false
	}
	return source, bounds, true
}

// newJob returns a running job for req.
func (h *WebHandler) newJob(req jobRequest, bounds *tilecalc.GeographicBounds, cancel context.CancelFunc) *job {
	total := 0
	for z := req.MinZoom; z <= req.MaxZoom; z++ {
		total += h.calc.CalculateTileBounds(bounds, z).TileCount()
	}
	id := newJobID()
	return &job{
		id:     id,
		cancel: cancel,
		// The buffer lets the engine continue while the event goroutine sends to subscribers.
		progress:  make(chan progressEvent, 64),
		result:    make(chan jobResult, 1),
		subscribe: make(chan *subscriber),
		done:      make(chan struct{}),
		record: jobRecord{
			ID:         id,
			Source:     req.Source,
			Bounds:     req.Bounds,
			MinZoom:    req.MinZoom,
			MaxZoom:    req.MaxZoom,
			State:      jobRunning,
			OutputFile: download.GenerateOutputFilename(req.Source, bounds, req.MinZoom, req.MaxZoom, req.Name),
			Total:      total,
			CreatedAt:  time.Now().UTC(),
		},
	}
}

// runJob runs the engine for j, frees the job lock and sends the result to the event goroutine.
func (h *WebHandler) runJob(ctx context.Context, j *job, req jobRequest, source *xmlparser.MapSource, bounds *tilecalc.GeographicBounds) {
	engine := download.NewEngine(req.engineConfig())
	outputPath := filepath.Join(h.outputDir, j.snapshot().OutputFile)
	_, err := engine.DownloadTiles(ctx, source, bounds, zoomLevelsBetween(req.MinZoom, req.MaxZoom), outputPath,
		func(p *download.Progress) bool {
			j.progress <- progressEvent{
				Downloaded: p.Downloaded,
				Failed:     p.Failed,
				Total:      p.TotalTiles,
				Rate:       p.TilesPerSecond(),
				ETASeconds: p.ETASeconds(),
			}
			return ctx.Err() == nil
		})

	res := jobResult{state: jobComplete}
	switch {
	case ctx.Err() != nil:
		res.state = jobCancelled
	case err != nil:
		res = jobResult{state: jobFailed, err: err}
	}

	// The lock frees first, so a client that reads the final state can start the next job at once.
	h.dlState.Lock()
	h.dlState.downloading = false
	h.dlState.cancel = nil
	h.dlState.Unlock()
	j.result <- res
}

// findJob returns the current job when its id is id. It writes 404 job_not_found and returns nil when it is not.
func (h *WebHandler) findJob(w http.ResponseWriter, id string) *job {
	h.dlState.Lock()
	j := h.dlState.job
	h.dlState.Unlock()
	if j == nil || j.id != id {
		writeError(w, http.StatusNotFound, "job_not_found", "Job not found")
		return nil
	}
	return j
}

// getJob returns the job record (FR-security-hardening-9).
func (h *WebHandler) getJob(w http.ResponseWriter, r *http.Request) {
	if j := h.findJob(w, r.PathValue("id")); j != nil {
		writeJSON(w, j.snapshot())
	}
}

// cancelJob cancels the job and returns 202 with its record (FR-security-hardening-11).
// The record still shows the running state, and the final state event shows the cancel.
func (h *WebHandler) cancelJob(w http.ResponseWriter, r *http.Request) {
	if j := h.findJob(w, r.PathValue("id")); j != nil {
		j.cancel()
		writeAccepted(w, j.snapshot())
	}
}

// streamJob sends the events of the job as SSE (FR-security-hardening-10).
// The first event is a state event with the current record (FR-security-hardening-15).
// The stream ends after the final state event. When the client leaves, the job continues.
func (h *WebHandler) streamJob(w http.ResponseWriter, r *http.Request) {
	j := h.findJob(w, r.PathValue("id"))
	if j == nil {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	rc := http.NewResponseController(w)

	s := &subscriber{events: make(chan sseMessage, 16), gone: make(chan struct{})}
	defer close(s.gone)
	select {
	case j.subscribe <- s:
	case <-j.done:
		// The job ended, so its event goroutine is gone and the record is final.
		_ = writeEvent(w, rc, sseMessage{"state", j.snapshot()}) // A failed write means that the client left.
		return
	}
	for {
		select {
		case m, open := <-s.events:
			if !open {
				return
			}
			if writeEvent(w, rc, m) != nil {
				return // The client left.
			}
		case <-r.Context().Done():
			return
		}
	}
}

// writeEvent writes m in the SSE wire format and flushes it to the client.
// It returns the write error when the client left.
func writeEvent(w io.Writer, rc *http.ResponseController, m sseMessage) error {
	data, err := json.Marshal(m.data)
	if err != nil {
		return fmt.Errorf("encode the %s event: %w", m.name, err)
	}
	// json.Marshal output holds no newline, so one data line carries the event.
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.name, data); err != nil {
		return fmt.Errorf("write the %s event: %w", m.name, err)
	}
	if err := rc.Flush(); err != nil {
		return fmt.Errorf("flush the %s event: %w", m.name, err)
	}
	return nil
}

// writeAccepted writes rec as a JSON response body with status 202.
// A failed write means that the client closed the connection, so the error is not used.
func writeAccepted(w http.ResponseWriter, rec jobRecord) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(rec)
}
