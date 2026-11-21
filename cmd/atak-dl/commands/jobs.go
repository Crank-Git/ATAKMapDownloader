package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/output"
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
	AcceptTerms      *bool     `json:"accept_terms"`
	// RateLimit lowers the request rate in requests per second. The engine ignores a value above the policy rate.
	RateLimit *float64 `json:"rate_limit"`
	// IfExists is the rule for an existing output file. An empty value means ifExistsFail.
	IfExists ifExists `json:"if_exists"`
	// OutputFormat is one of output.Formats. checkJobRequest sets an empty value to output.MBTiles (FR-output-formats-3).
	OutputFormat string `json:"output_format"`
}

// engineConfig returns the engine configuration for the request.
func (req jobRequest) engineConfig() download.Config {
	cfg := download.DefaultConfig()
	cfg.Format = req.OutputFormat
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
	if req.RateLimit != nil {
		cfg.RateLimit = *req.RateLimit
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
	// Skipped counts the tiles that the output file already holds, so only a resume makes it above 0.
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
	// Progress events leave finalSlots free in its buffer, so no send to it blocks.
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

	// warning is the deprecation warning of a v0.1 map source name, or empty.
	// Each subscriber gets it as a log event, so the job record keeps its shape.
	warning string

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
			// A new subscriber has an empty buffer, so these sends do not block.
			s.events <- sseMessage{"state", j.snapshot()}
			if j.warning != "" {
				s.events <- sseMessage{"log", logEvent{Level: "warning", Message: j.warning}}
			}
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
	j.record.Downloaded, j.record.Skipped, j.record.Failed = p.Downloaded, p.Skipped, p.Failed
}

// finalSlots is the number of events that finish sends to one subscriber: the error log event and the final state event.
const finalSlots = 2

// offer sends m to s. It drops m when the buffer of s has no free slot outside the finalSlots.
// The next progress event carries the counts again, so a dropped one loses no count.
// It removes s from subs when the stream of s ended.
func offer(subs map[*subscriber]struct{}, s *subscriber, m sseMessage) {
	select {
	case <-s.gone:
		delete(subs, s)
	default:
		if len(s.events) < cap(s.events)-finalSlots {
			s.events <- m
		}
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

	// The free slots hold these events, so a subscriber that stops reading does not block the job.
	for s := range subs {
		if res.state == jobFailed {
			s.events <- sseMessage{"log", logEvent{Level: "error", Message: final.Error}}
		}
		s.events <- sseMessage{"state", final}
		close(s.events)
	}
	close(j.done)
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
// It returns 409 output_exists when the if_exists rule does not accept the existing output file (FR-robust-downloads-14).
func (h *WebHandler) createJob(w http.ResponseWriter, r *http.Request) {
	var req jobRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	source, bounds, warning, ok := h.checkJobRequest(w, &req)
	if !ok {
		return
	}

	h.dlState.Lock()
	if h.dlState.downloading {
		h.dlState.Unlock()
		writeError(w, http.StatusConflict, "job_running", "A job is already running")
		return
	}
	// The job lock is held, so no other job writes the output file between the check and the start.
	if !h.checkOutput(w, req, source, bounds) {
		h.dlState.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := h.newJob(req, bounds, cancel)
	j.warning = warning
	h.dlState.downloading = true
	h.dlState.job = j
	h.dlState.Unlock()

	go j.sendEvents()
	go h.runJob(ctx, j, req, source, bounds)
	writeAccepted(w, j.snapshot())
}

// checkOutput applies the if_exists rule of req to its output file.
// It writes the error response and returns false when the rule does not accept the file or the check fails.
// The API has no force field, so a resume accepts a v0.1 output file with no map source name.
func (h *WebHandler) checkOutput(w http.ResponseWriter, req jobRequest, source *xmlparser.MapSource, bounds *tilecalc.GeographicBounds) bool {
	rule := req.IfExists
	if rule == "" {
		rule = ifExistsFail
	}
	path := filepath.Join(h.outputDir, download.GenerateOutputFilename(req.Source, bounds, req.MinZoom, req.MaxZoom, req.Name, req.OutputFormat))
	err := prepareOutput(path, rule, source.Name, req.OutputFormat, true)
	var conflict *outputConflict
	switch {
	case errors.Is(err, errOutputExists):
		writeError(w, http.StatusConflict, "output_exists", `Output file exists; send "if_exists": "resume" or "overwrite"`)
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, "output_exists", conflict.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
	return err == nil
}

// checkJobRequest returns the map source and the bounds of req.
// When req names a v0.1 map source, it sets req.Source to the v2 name and returns the deprecation warning.
// It writes the error response and returns false when req is not valid.
func (h *WebHandler) checkJobRequest(w http.ResponseWriter, req *jobRequest) (*xmlparser.MapSource, *tilecalc.GeographicBounds, string, bool) {
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "unknown_source", "Source name is required")
		return nil, nil, "", false
	}
	if len(req.Bounds) != 4 {
		writeError(w, http.StatusBadRequest, "invalid_bounds", "Bounds must hold four numbers: west, south, east, north")
		return nil, nil, "", false
	}
	source, warning, err := findSource(h.parser, req.Source)
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown_source", err.Error())
		return nil, nil, "", false
	}
	if source == nil {
		writeError(w, http.StatusNotFound, "unknown_source", "Source not found")
		return nil, nil, "", false
	}
	// The job record, the output file name and the job all use the v2 name.
	req.Source = source.Name
	bounds, err := tilecalc.NewGeographicBounds(req.Bounds[1], req.Bounds[3], req.Bounds[0], req.Bounds[2])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_bounds", err.Error())
		return nil, nil, "", false
	}
	if code, message := checkJobLimits(source, bounds, req.MinZoom, req.MaxZoom, req.engineConfig().MaxConcurrent, h.maxTiles); code != "" {
		writeError(w, http.StatusBadRequest, code, message)
		return nil, nil, "", false
	}
	// Zero rate_limit keeps the policy rate in the engine, so the API rejects it with the negative values.
	if req.RateLimit != nil && *req.RateLimit <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_rate_limit", fmt.Sprintf("rate_limit %g is not above 0", *req.RateLimit))
		return nil, nil, "", false
	}
	if !checkOutputOptions(w, req) {
		return nil, nil, "", false
	}
	if entry, restricted := restrictedEntry(source); restricted && (req.AcceptTerms == nil || !*req.AcceptTerms) {
		writeError(w, http.StatusForbidden, "terms_not_accepted", fmt.Sprintf(
			"%s (%s); send \"accept_terms\": true to accept the terms", entry.Reason, entry.ReferenceURL))
		return nil, nil, "", false
	}
	return source, bounds, warning, true
}

// checkOutputOptions checks the output_format and if_exists fields of req, and it sets an empty output_format to output.MBTiles.
// It writes the error response and returns false when a field is not valid.
func checkOutputOptions(w http.ResponseWriter, req *jobRequest) bool {
	if req.OutputFormat == "" {
		req.OutputFormat = output.MBTiles
	}
	if code, message := checkFormat("output_format", req.OutputFormat); code != "" {
		writeError(w, http.StatusBadRequest, code, message)
		return false
	}
	switch req.IfExists {
	case "", ifExistsFail, ifExistsResume, ifExistsOverwrite:
		return true
	default:
		writeError(w, http.StatusBadRequest, "invalid_if_exists", fmt.Sprintf("if_exists %q is not fail, resume or overwrite", req.IfExists))
		return false
	}
}

// maxConcurrency is the highest concurrency that a job accepts (FR-security-hardening-20).
const maxConcurrency = 32

// defaultMaxTiles is the default tile cap of download --max-tiles and serve --max-tiles (FR-robust-downloads-26).
const defaultMaxTiles = 500_000

// checkJobLimits returns an error code and a message when the zoom range, the concurrency or the tile count of a job is not valid.
// The tile count is the count of the tiles of bounds from minZoom to maxZoom. It returns these codes:
//   - "zoom_out_of_range" when the range from minZoom to maxZoom is empty or outside the zoom range of source.
//   - "invalid_concurrency" when concurrency is below 1 or above maxConcurrency.
//   - "too_many_tiles" when the tile count is above maxTiles (FR-robust-downloads-26, -27).
//
// It returns an empty code when the job is valid.
func checkJobLimits(source *xmlparser.MapSource, bounds *tilecalc.GeographicBounds, minZoom, maxZoom, concurrency, maxTiles int) (code, message string) {
	if minZoom > maxZoom {
		return "zoom_out_of_range", fmt.Sprintf("min_zoom %d is above max_zoom %d", minZoom, maxZoom)
	}
	if minZoom < source.MinZoom || maxZoom > source.MaxZoom {
		return "zoom_out_of_range", fmt.Sprintf("zoom range %d-%d is outside the source zoom range %d-%d",
			minZoom, maxZoom, source.MinZoom, source.MaxZoom)
	}
	// The engine semaphore holds concurrency slots, so 0 slots stop the job forever.
	if concurrency < 1 || concurrency > maxConcurrency {
		return "invalid_concurrency", fmt.Sprintf("concurrency %d is outside the range 1-%d", concurrency, maxConcurrency)
	}
	// The check runs before any request, so a job above the tile cap sends none.
	if tiles := tileCount(bounds, minZoom, maxZoom); tiles > maxTiles {
		return "too_many_tiles", fmt.Sprintf("the job has %d tiles, and the tile cap is %d", tiles, maxTiles)
	}
	return "", ""
}

// tileCount returns the count of the tiles of bounds at every zoom level from minZoom to maxZoom.
func tileCount(bounds *tilecalc.GeographicBounds, minZoom, maxZoom int) int {
	calc := tilecalc.NewTileCalculator()
	total := 0
	for z := minZoom; z <= maxZoom; z++ {
		total += calc.CalculateTileBounds(bounds, z).TileCount()
	}
	return total
}

// newJob returns a running job for req.
func (h *WebHandler) newJob(req jobRequest, bounds *tilecalc.GeographicBounds, cancel context.CancelFunc) *job {
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
			OutputFile: download.GenerateOutputFilename(req.Source, bounds, req.MinZoom, req.MaxZoom, req.Name, req.OutputFormat),
			Total:      tileCount(bounds, req.MinZoom, req.MaxZoom),
			CreatedAt:  time.Now().UTC(),
		},
	}
}

// runJob runs the engine for j, frees the job lock and sends the result to the event goroutine.
func (h *WebHandler) runJob(ctx context.Context, j *job, req jobRequest, source *xmlparser.MapSource, bounds *tilecalc.GeographicBounds) {
	cfg := req.engineConfig()
	cfg.UserAgent = h.userAgent
	cfg.Policy = sourcePolicy(source)
	engine := download.NewEngine(cfg)
	outputPath := filepath.Join(h.outputDir, j.snapshot().OutputFile)
	_, err := engine.DownloadTiles(ctx, source, bounds, zoomLevelsBetween(req.MinZoom, req.MaxZoom), outputPath,
		func(p download.Progress) bool {
			j.progress <- progressEvent{
				Downloaded: p.Downloaded,
				Skipped:    p.Skipped,
				Failed:     p.Failed,
				Total:      p.TotalTiles,
				Rate:       p.TilesPerSecond(),
				ETASeconds: p.ETASeconds(),
			}
			return ctx.Err() == nil
		})

	res := jobResult{state: jobComplete}
	switch {
	case errors.Is(err, download.ErrWriteFailed):
		// The output file lost tiles, so a write failure outranks a cancel.
		res = jobResult{state: jobFailed, err: err}
	case ctx.Err() != nil:
		res.state = jobCancelled
	case err != nil:
		res = jobResult{state: jobFailed, err: err}
	}

	// The lock frees first, so a client that reads the final state can start the next job at once.
	h.dlState.Lock()
	h.dlState.downloading = false
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
// A running job can still show the running state, because the engine stops after the cancel.
// Its final state event shows the cancelled state. A job that ended keeps its final state.
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
		// The final state event comes last, because the client reads the last event as the result.
		if j.warning != "" && writeEvent(w, rc, sseMessage{"log", logEvent{Level: "warning", Message: j.warning}}) != nil {
			return // The client left.
		}
		_ = writeEvent(w, rc, sseMessage{"state", j.snapshot()}) // A failed write means that the client left.
		return
	case <-r.Context().Done():
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
