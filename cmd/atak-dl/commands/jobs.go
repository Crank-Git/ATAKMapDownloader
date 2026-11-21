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
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/crank-git/atak-map-downloader/internal/datapackage"
	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/jobs"
	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

// jobRequest is the body of POST /api/jobs, with the schema of docs/specs/features/05-security-hardening.md.
// A nil pointer field means that the client did not send the setting, so the engine default applies.
type jobRequest struct {
	Source string `json:"source"`
	// Areas is the area list of the job. With no areas, Bounds, MinZoom and MaxZoom give one bounds area (FR-area-selection-26).
	Areas            []areaRequest `json:"areas"`
	Bounds           []float64     `json:"bounds"`
	MinZoom          int           `json:"min_zoom"`
	MaxZoom          int           `json:"max_zoom"`
	Name             string        `json:"name"`
	Concurrency      *int          `json:"concurrency"`
	FailureTolerance *float64      `json:"failure_tolerance"`
	IgnoreErrors     *bool         `json:"ignore_errors"`
	VerifyTLS        *bool         `json:"verify_tls"`
	AcceptTerms      *bool         `json:"accept_terms"`
	// RateLimit lowers the request rate in requests per second. The engine ignores a value above the policy rate.
	RateLimit *float64 `json:"rate_limit"`
	// IfExists is the rule for an existing output file. An empty value means ifExistsFail.
	IfExists ifExists `json:"if_exists"`
	// OutputFormat is one of output.Formats. checkJobRequest sets an empty value to output.MBTiles (FR-output-formats-3).
	OutputFormat string `json:"output_format"`
	// DataPackage wraps the output file in the data package <package_name>.zip (FR-output-formats-19).
	// Without it, the job ignores IncludeSource and PackageName.
	DataPackage bool `json:"data_package"`
	// IncludeSource adds the source file of the map source to the data package (FR-output-formats-23).
	IncludeSource bool `json:"include_source"`
	// PackageName is the package name. checkJobRequest accepts it only when datapackage.ValidName does.
	PackageName string `json:"package_name"`
	// Label is the label of the submitter, 1 to 80 characters (FR-team-server-13). A nil Label means no label.
	Label *string `json:"label,omitempty"`
}

// packageOptions returns the data package settings of the request, or nil when the job writes no data package.
func (req jobRequest) packageOptions() *packageOptions {
	if !req.DataPackage {
		return nil
	}
	return &packageOptions{name: req.PackageName, includeSource: req.IncludeSource}
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

// jobState is the state of a job.
type jobState string

const (
	// jobQueued is the state of a job from POST /api/jobs until a worker starts it (FR-team-server-1).
	jobQueued    jobState = "queued"
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
	// StartedAt is the time when a worker started the job, or nil before the start.
	StartedAt *time.Time `json:"started_at"`
	// FinishedAt is the time of the final state, or nil before it.
	FinishedAt *time.Time `json:"finished_at"`
	Error      string     `json:"error"`
	// Reason is server_restarted for a job that was running when the server stopped (FR-team-server-9).
	Reason string `json:"reason,omitempty"`
	// Label is the label of the job request (FR-team-server-13).
	Label string `json:"label,omitempty"`
	// Position is the queue position of a queued job, 1 for the next job. A read computes it, so it is 0 in every other state.
	Position int `json:"position,omitempty"`
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
	id string
	// ctx exists from the creation of the job, so a DELETE after a worker took the job still stops it before the engine runs.
	ctx    context.Context
	cancel context.CancelFunc

	// req, source and areas are the checked request, because the worker runs the job after POST /api/jobs returned.
	req    jobRequest
	source *xmlparser.MapSource
	areas  []tilecalc.JobArea
	// queue gives the position of a queued job.
	queue *jobs.Queue
	// store keeps each state of the job in jobs.jsonl.
	store *jobs.Store
	// started closes when a worker starts the job, so the event goroutine sends the running state.
	started chan struct{}

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

// snapshot returns a copy of the job record, with the queue position of a queued job.
func (j *job) snapshot() jobRecord {
	j.mu.Lock()
	rec := j.record
	j.mu.Unlock()
	if rec.State == jobQueued {
		rec.Position = j.queue.Position(j.id)
	}
	return rec
}

// sendEvents sends the events of the job to its subscribers until the job ends.
func (j *job) sendEvents() {
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()
	subs := map[*subscriber]struct{}{}
	var latest progressEvent
	pending := false
	started := j.started
	for {
		select {
		case <-started:
			// A nil channel blocks, so the running state goes out once.
			started = nil
			rec := j.snapshot()
			for s := range subs {
				offer(subs, s, sseMessage{"state", rec})
			}
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
	now := time.Now().UTC()
	j.mu.Lock()
	j.record.State = res.state
	if res.state == jobFailed {
		j.record.Error = res.err.Error()
	}
	j.record.FinishedAt = &now
	final := j.record
	j.mu.Unlock()
	j.save(final)

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
	mux.HandleFunc("GET /api/jobs", h.listJobs)
	mux.HandleFunc("POST /api/jobs", h.createJob)
	mux.HandleFunc("GET /api/jobs/{id}", h.getJob)
	mux.HandleFunc("GET /api/jobs/{id}/events", h.streamJob)
	mux.HandleFunc("DELETE /api/jobs/{id}", h.cancelJob)
	mux.HandleFunc("POST /api/jobs/{id}/resume", h.resumeJob)
	notAllowed := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
	mux.HandleFunc("/api/jobs", notAllowed)
	mux.HandleFunc("/api/jobs/{id}", notAllowed)
	mux.HandleFunc("/api/jobs/{id}/events", notAllowed)
	mux.HandleFunc("/api/jobs/{id}/resume", notAllowed)
	mux.HandleFunc("/api/jobs/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Route not found")
	})
}

// createJob adds a job to the queue and returns 202 with its record (FR-security-hardening-8, FR-team-server-1).
// When a queued or running job writes the same output file name, the new job gets the suffix -2, -3 and so on.
// It returns 409 output_exists when the if_exists rule does not accept the existing output file (FR-robust-downloads-14).
// It returns 429 queue_full when the queue is full (FR-team-server-6).
func (h *WebHandler) createJob(w http.ResponseWriter, r *http.Request) {
	var req jobRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	source, areas, warning, ok := h.checkJobRequest(w, &req)
	if !ok {
		return
	}
	// The lock covers the name choice and the enqueue, so two submits never choose one name.
	h.jobMu.Lock()
	defer h.jobMu.Unlock()
	var outputFile string
	outputFile, req.PackageName = h.uniqueOutput(jobOutputFile(req.Source, areas, req.Name, req.OutputFormat), req)
	if !h.checkOutput(w, req, source, outputFile) {
		return
	}
	j := h.newJob(newJobID(), req, source, areas, outputFile)
	j.warning = warning
	h.writeEnqueue(w, j)
}

// writeEnqueue calls enqueue for j and writes 202 with the record, or 429 queue_full. The caller holds h.jobMu.
func (h *WebHandler) writeEnqueue(w http.ResponseWriter, j *job) {
	rec, err := h.enqueue(j)
	if err != nil {
		writeError(w, http.StatusTooManyRequests, "queue_full", fmt.Sprintf("The queue holds %d jobs", jobs.MaxQueued))
		return
	}
	writeAccepted(w, rec)
}

// enqueue puts j in the job map and in the queue, stores its queued state, starts its event goroutine and returns its record.
// It replaces a job with the same id, so a resume keeps the id. The caller holds h.jobMu.
// A worker reads the job map under h.jobMu before it stores the running state, so the queued line comes first in jobs.jsonl.
// It returns jobs.ErrQueueFull when the queue is full. The job map then keeps the job that it had.
func (h *WebHandler) enqueue(j *job) (jobRecord, error) {
	// The snapshot comes before Add, so the record shows the queued state even when a worker starts the job at once.
	rec := j.snapshot()
	old, had := h.jobs[j.id]
	// A worker can take the job as soon as Add returns, so the map holds the job first.
	h.jobs[j.id] = j
	pos, err := h.queue.Add(j.id)
	if err != nil {
		if had {
			h.jobs[j.id] = old
		} else {
			delete(h.jobs, j.id)
		}
		return jobRecord{}, err
	}
	j.save(rec)
	go j.sendEvents()
	rec.Position = pos
	return rec, nil
}

// checkOutput applies the if_exists rule of req to its output file, the file outputFile in the output directory.
// It writes the error response and returns false when the rule does not accept the file or the check fails.
// It accepts the rule overwrite with no check, because the delete of the file waits for the start of the job.
func (h *WebHandler) checkOutput(w http.ResponseWriter, req jobRequest, source *xmlparser.MapSource, outputFile string) bool {
	// A queued job can name the output file of a running job, so a delete now can remove a file in use.
	if req.IfExists == ifExistsOverwrite {
		return true
	}
	err := h.prepareJobOutput(req, source, outputFile)
	var conflict *outputConflict
	switch {
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, "output_exists", outputErrorMessage(err))
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
	return err == nil
}

// prepareJobOutput applies the if_exists rule of req to the output file outputFile and to its data package.
// It returns the errors of prepareDataPackage and prepareOutput.
// The API has no force field, so a resume accepts a v0.1 output file with no map source name.
func (h *WebHandler) prepareJobOutput(req jobRequest, source *xmlparser.MapSource, outputFile string) error {
	rule := req.IfExists
	if rule == "" {
		rule = ifExistsFail
	}
	path := filepath.Join(h.outputDir, outputFile)
	if pkg := req.packageOptions(); pkg != nil {
		if err := prepareDataPackage(pkg.zipPath(path), rule); err != nil {
			return err
		}
	}
	return prepareOutput(path, rule, source.Name, req.OutputFormat, true)
}

// outputErrorMessage returns the job API message for an error of prepareJobOutput.
// The message of errOutputExists names CLI flags, so the API names its own if_exists field.
func outputErrorMessage(err error) string {
	if errors.Is(err, errOutputExists) {
		return `Output file exists; send "if_exists": "resume" or "overwrite"`
	}
	return err.Error()
}

// checkJobRequest returns the map source and the areas of req.
// When req names a v0.1 map source, it sets req.Source to the v2 name and returns the deprecation warning.
// It writes the error response and returns false when req is not valid.
func (h *WebHandler) checkJobRequest(w http.ResponseWriter, req *jobRequest) (*xmlparser.MapSource, []tilecalc.JobArea, string, bool) {
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "unknown_source", "Source name is required")
		return nil, nil, "", false
	}
	areas, code, message := requestAreas(req.Bounds, req.MinZoom, req.MaxZoom, req.Areas)
	if code != "" {
		writeError(w, http.StatusBadRequest, code, message)
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
	if code, message := checkJobLimits(source, areas, req.engineConfig().MaxConcurrent, h.maxTiles); code != "" {
		writeError(w, http.StatusBadRequest, code, message)
		return nil, nil, "", false
	}
	// Zero rate_limit keeps the policy rate in the engine, so the API rejects it with the negative values.
	if req.RateLimit != nil && *req.RateLimit <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_rate_limit", fmt.Sprintf("rate_limit %g is not above 0", *req.RateLimit))
		return nil, nil, "", false
	}
	if !checkOutputOptions(w, req, source) {
		return nil, nil, "", false
	}
	if req.Label != nil && (*req.Label == "" || utf8.RuneCountInString(*req.Label) > maxLabel) {
		writeError(w, http.StatusBadRequest, "invalid_label", fmt.Sprintf("label is not 1 to %d characters", maxLabel))
		return nil, nil, "", false
	}
	if entry, restricted := restrictedEntry(source); restricted && (req.AcceptTerms == nil || !*req.AcceptTerms) {
		writeError(w, http.StatusForbidden, "terms_not_accepted", fmt.Sprintf(
			"%s (%s); send \"accept_terms\": true to accept the terms", entry.Reason, entry.ReferenceURL))
		return nil, nil, "", false
	}
	return source, areas, warning, true
}

// checkOutputOptions checks the output_format, if_exists and package_name fields of req for a job of source.
// It sets an empty output_format to output.MBTiles, and it trims the spaces around package_name.
// It checks package_name only when data_package is true.
// It writes the error response and returns false when a field is not valid.
func checkOutputOptions(w http.ResponseWriter, req *jobRequest, source *xmlparser.MapSource) bool {
	if req.OutputFormat == "" {
		req.OutputFormat = output.MBTiles
	}
	if code, message := checkFormat("output_format", req.OutputFormat, source.TileType); code != "" {
		writeError(w, http.StatusBadRequest, code, message)
		return false
	}
	// The web UI trims the name too, so the CLI, the API and the preview give the same zip name.
	req.PackageName = strings.TrimSpace(req.PackageName)
	if req.DataPackage && !datapackage.ValidName(req.PackageName) {
		writeError(w, http.StatusBadRequest, "invalid_package_name", packageNameMessage("package_name", req.PackageName))
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

// maxLabel is the highest character count of a job label (FR-team-server-13).
const maxLabel = 80

// maxConcurrency is the highest concurrency that a job accepts (FR-security-hardening-20).
const maxConcurrency = 32

// defaultMaxTiles is the default tile cap of download --max-tiles and serve --max-tiles (FR-robust-downloads-26).
const defaultMaxTiles = 500_000

// checkJobLimits returns an error code and a message when the areas, the concurrency or the tile count of a job is not valid.
// The tile count is the count of unique tiles over all areas (FR-area-selection-9). It returns these codes:
//   - The codes of checkAreas.
//   - "zoom_out_of_range" when the zoom range of an area is outside the zoom range of source.
//   - "invalid_concurrency" when concurrency is below 1 or above maxConcurrency.
//   - "too_many_tiles" when the tile count is above maxTiles (FR-robust-downloads-26, -27).
//
// It returns an empty code when the job is valid.
func checkJobLimits(source *xmlparser.MapSource, areas []tilecalc.JobArea, concurrency, maxTiles int) (code, message string) {
	if code, message := checkAreas(areas); code != "" {
		return code, message
	}
	if minZoom, maxZoom := tilecalc.JobZoomRange(areas); minZoom < source.MinZoom || maxZoom > source.MaxZoom {
		return "zoom_out_of_range", fmt.Sprintf("zoom range %d-%d is outside the source zoom range %d-%d",
			minZoom, maxZoom, source.MinZoom, source.MaxZoom)
	}
	// The engine semaphore holds concurrency slots, so 0 slots stop the job forever.
	if concurrency < 1 || concurrency > maxConcurrency {
		return "invalid_concurrency", fmt.Sprintf("concurrency %d is outside the range 1-%d", concurrency, maxConcurrency)
	}
	// The check runs before any request, so a job above the tile cap sends none.
	if tiles := tilecalc.CountJobTiles(areas).Unique; tiles > maxTiles {
		return "too_many_tiles", fmt.Sprintf("the job has %d tiles, and the tile cap is %d", tiles, maxTiles)
	}
	return "", ""
}

// newJob returns a queued job with the id id for req, which writes the output file outputFile.
// The record holds the rectangle and the zoom range over all areas.
func (h *WebHandler) newJob(id string, req jobRequest, source *xmlparser.MapSource, areas []tilecalc.JobArea, outputFile string) *job {
	b := tilecalc.JobBound(areas)
	minZoom, maxZoom := tilecalc.JobZoomRange(areas)
	// The job outlives the request, so its context does not come from the request.
	ctx, cancel := context.WithCancel(context.Background())
	return &job{
		id:      id,
		ctx:     ctx,
		cancel:  cancel,
		req:     req,
		source:  source,
		areas:   areas,
		queue:   h.queue,
		store:   h.store,
		started: make(chan struct{}),
		// The buffer lets the engine continue while the event goroutine sends to subscribers.
		progress:  make(chan progressEvent, 64),
		result:    make(chan jobResult, 1),
		subscribe: make(chan *subscriber),
		done:      make(chan struct{}),
		record: jobRecord{
			ID:         id,
			Source:     req.Source,
			Bounds:     []float64{b.Min[0], b.Min[1], b.Max[0], b.Max[1]},
			MinZoom:    minZoom,
			MaxZoom:    maxZoom,
			State:      jobQueued,
			OutputFile: outputFile,
			Total:      tilecalc.CountJobTiles(areas).Unique,
			CreatedAt:  time.Now().UTC(),
			Label:      label(req),
		},
	}
}

// label returns the label of req, or an empty string when req has no label.
func label(req jobRequest) string {
	if req.Label == nil {
		return ""
	}
	return *req.Label
}

// startJob is the run func of the queue. It runs the job id on the worker that took it, and returns when the job ends.
// A job that a DELETE cancelled before the start ends cancelled with no tile request.
func (h *WebHandler) startJob(id string) {
	h.jobMu.Lock()
	j := h.jobs[id]
	h.jobMu.Unlock()
	if j.ctx.Err() != nil {
		j.result <- jobResult{state: jobCancelled}
		return
	}
	now := time.Now().UTC()
	j.mu.Lock()
	j.record.State = jobRunning
	j.record.StartedAt = &now
	rec := j.record
	j.mu.Unlock()
	j.save(rec)
	close(j.started)
	// A file can appear while the job is queued, so the if_exists rule applies again at the start.
	if err := h.prepareJobOutput(j.req, j.source, rec.OutputFile); err != nil {
		j.result <- jobResult{state: jobFailed, err: errors.New(outputErrorMessage(err))}
		return
	}
	j.result <- h.runJob(j)
}

// runJob runs the engine for j and returns the result.
func (h *WebHandler) runJob(j *job) jobResult {
	ctx, req, source := j.ctx, j.req, j.source
	cfg := req.engineConfig()
	cfg.UserAgent = h.userAgent
	cfg.Policy = sourcePolicy(source)
	engine := download.NewEngine(cfg)
	outputPath := filepath.Join(h.outputDir, j.snapshot().OutputFile)
	_, err := engine.DownloadTiles(ctx, source, j.areas, outputPath,
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
	// A cancelled or failed job has an incomplete output file, so only a complete job gets a data package.
	if pkg := req.packageOptions(); pkg != nil && res.state == jobComplete {
		if zipPath, perr := writeDataPackage(outputPath, pkg, source); perr != nil {
			res = jobResult{state: jobFailed, err: perr}
		} else {
			j.mu.Lock()
			j.record.OutputFile = filepath.Base(zipPath)
			j.mu.Unlock()
		}
	}
	return res
}

// findJob returns the job with the id id. It writes 404 job_not_found and returns nil when no job has that id.
func (h *WebHandler) findJob(w http.ResponseWriter, id string) *job {
	h.jobMu.Lock()
	j := h.jobs[id]
	h.jobMu.Unlock()
	if j == nil {
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
// A queued job leaves the queue and ends cancelled (FR-team-server-4).
// The record can still show the queued or the running state, because the job stops after the response.
// Its final state event shows the cancelled state. A job that ended keeps its final state.
func (h *WebHandler) cancelJob(w http.ResponseWriter, r *http.Request) {
	j := h.findJob(w, r.PathValue("id"))
	if j == nil {
		return
	}
	// No worker takes a removed job, so this send is the only result of the job.
	if h.queue.Remove(j.id) {
		j.result <- jobResult{state: jobCancelled}
	}
	j.cancel()
	writeAccepted(w, j.snapshot())
}

// resumeJob adds a failed or cancelled job to the queue again with the if_exists rule resume (FR-team-server-5).
// It returns 202 with the record. The job keeps its id, its request and its output file.
// The engine skips the tiles that the output file holds, so the job requests only the missing tiles.
// It returns 404 job_not_found for an unknown id, 409 invalid_state for a job in another state, and 429 queue_full.
// It returns 409 output_in_use when a queued or running job writes the output file or the data package of the job.
func (h *WebHandler) resumeJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// The lock covers the state check and the replace, so two resumes of one job never queue it twice.
	h.jobMu.Lock()
	defer h.jobMu.Unlock()
	old := h.jobs[id]
	if old == nil {
		writeError(w, http.StatusNotFound, "job_not_found", "Job not found")
		return
	}
	prev := old.snapshot()
	if prev.State != jobFailed && prev.State != jobCancelled {
		writeError(w, http.StatusConflict, "invalid_state", fmt.Sprintf("job state is %s; only a failed or cancelled job resumes", prev.State))
		return
	}
	// A job of jobs.jsonl can name a map source that the server no longer has.
	if old.source == nil {
		writeError(w, http.StatusConflict, "invalid_state", fmt.Sprintf("job source %q is not found; the job cannot resume", prev.Source))
		return
	}
	// A job that a member submitted while this job was not active can hold its output file name.
	if holdsOutput(h.heldNames(), prev.OutputFile, old.req) {
		writeError(w, http.StatusConflict, "output_in_use", fmt.Sprintf("a queued or running job writes the output file %s", prev.OutputFile))
		return
	}
	req := old.req
	req.IfExists = ifExistsResume
	j := h.newJob(id, req, old.source, old.areas, prev.OutputFile)
	j.warning = old.warning
	j.record.CreatedAt = prev.CreatedAt
	h.writeEnqueue(w, j)
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
