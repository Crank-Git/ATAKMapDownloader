package commands

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/datapackage"
	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/jobs"
	"github.com/spf13/cobra"
)

// storedJob is one line of jobs.jsonl: the job record and the job request (FR-team-server-7).
// A queued job runs after a restart, so the line holds the request too. jobRequest has no token field.
type storedJob struct {
	jobRecord
	Request jobRequest `json:"request"`
}

// dataDir returns the data directory of cmd (FR-team-server-14).
// The directory comes from --data-dir, else from ATAKDL_DATA_DIR, else it is ./downloads.
func dataDir(cmd *cobra.Command) (string, error) {
	dir, err := cmd.Flags().GetString("data-dir")
	if err != nil {
		return "", fmt.Errorf("read --data-dir: %w", err)
	}
	if dir == "" {
		dir = os.Getenv("ATAKDL_DATA_DIR")
	}
	if dir == "" {
		dir = download.DownloadsDir()
	}
	return dir, nil
}

// checkWritable makes the directory dir and writes a probe file in it.
// It returns the error "data directory is not writable: <dir>" when either step fails.
func checkWritable(dir string) error {
	fail := func(err error) error {
		return fmt.Errorf("data directory is not writable: %s (%w)", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // The output files are for the operator and ATAK, so other users can read them.
		return fail(err)
	}
	f, err := os.CreateTemp(dir, ".atak-dl-write-check-*")
	if err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Remove(f.Name()); err != nil { //nolint:gosec // os.CreateTemp made the name in dir.
		return fail(err)
	}
	return nil
}

// useDataDir makes dir the directory of the output files and of jobs.jsonl, and reads the jobs of jobs.jsonl.
// Call it before the workers start, so a restored queued job runs after every other restored job is in the map.
// It returns the errors of checkWritable and jobs.OpenStore.
func (h *WebHandler) useDataDir(dir string) error {
	if err := checkWritable(dir); err != nil {
		return err
	}
	store, err := jobs.OpenStore(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		return fmt.Errorf("open the job store: %w", err)
	}
	h.outputDir = dir
	h.store = store
	h.restoreJobs()
	return nil
}

// restoreJobs puts each job of the store in the job map (FR-team-server-8).
// A running job becomes failed with reason server_restarted (FR-team-server-9).
// A queued job goes back in the queue in the order of creation (FR-team-server-10).
func (h *WebHandler) restoreJobs() {
	h.jobMu.Lock()
	defer h.jobMu.Unlock()
	var queued []*job
	for _, line := range h.store.Lines() {
		var s storedJob
		if err := json.Unmarshal(line, &s); err != nil {
			slog.Warn("skip a job of jobs.jsonl that does not decode", "error", err)
			continue
		}
		if j := h.restoreJob(s); j.record.State == jobQueued {
			queued = append(queued, j)
		}
	}
	slices.SortStableFunc(queued, func(a, b *job) int { return a.record.CreatedAt.Compare(b.record.CreatedAt) })
	for _, j := range queued {
		if _, err := h.enqueue(j); err != nil {
			h.endRestored(j, "", fmt.Errorf("restore the job: %w", err))
		}
	}
}

// restoreJob returns the job of s and puts it in the job map. The caller holds h.jobMu.
// A queued job keeps the queued state, and the caller adds it to the queue.
// A queued job whose map source or areas no longer resolve becomes failed.
func (h *WebHandler) restoreJob(s storedJob) *job {
	req := s.Request
	source, warning, err := findSource(h.parser, req.Source)
	if err == nil && source == nil {
		err = fmt.Errorf("source %q not found", req.Source)
	}
	areas, code, message := requestAreas(req.Bounds, req.MinZoom, req.MaxZoom, req.Areas)
	if err == nil && code != "" {
		err = fmt.Errorf("%s: %s", code, message)
	}
	j := h.newJob(s.ID, req, source, areas, s.OutputFile)
	j.warning = warning
	rec := s.jobRecord
	rec.Position = 0
	j.record = rec
	h.jobs[j.id] = j
	switch {
	case rec.State == jobRunning:
		h.endRestored(j, "server_restarted", errors.New("the server restarted while the job ran"))
	case rec.State == jobQueued && err != nil:
		h.endRestored(j, "", err)
	case rec.State != jobQueued:
		// The job ended before the restart, so no event goroutine runs for it.
		j.cancel()
		close(j.done)
	}
	return j
}

// endRestored sets the restored job j to failed with reason and the message of err, and stores the new state.
func (h *WebHandler) endRestored(j *job, reason string, err error) {
	now := time.Now().UTC()
	j.record.State = jobFailed
	j.record.Reason = reason
	j.record.Error = err.Error()
	j.record.FinishedAt = &now
	j.save(j.record)
	j.cancel()
	close(j.done)
}

// save appends rec and the request of j to jobs.jsonl (FR-team-server-7).
// The job continues after a failed write, so save only logs the error.
func (j *job) save(rec jobRecord) {
	rec.Position = 0
	line, err := json.Marshal(storedJob{jobRecord: rec, Request: j.req})
	if err == nil {
		err = j.store.Append(j.id, line)
	}
	if err != nil {
		slog.Error("write the job to jobs.jsonl", "job", j.id, "error", err)
	}
}

// jobsPerPage is the page size of GET /api/jobs (FR-team-server-11).
const jobsPerPage = 50

// jobList is the response of GET /api/jobs.
type jobList struct {
	Jobs  []jobRecord `json:"jobs"`
	Page  int         `json:"page"`
	Pages int         `json:"pages"`
}

// listJobs returns one page of the jobs, newest first, 50 per page (FR-team-server-11).
// The query parameter page is 1 for the first page. Page 1 is the default.
// A page after the last page has no jobs. It returns 400 invalid_query when page is not a whole number of 1 or more.
func (h *WebHandler) listJobs(w http.ResponseWriter, r *http.Request) {
	page := 1
	if r.URL.Query().Has("page") {
		n, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_query", fmt.Sprintf("page %q is not a whole number of 1 or more", r.URL.Query().Get("page")))
			return
		}
		page = n
	}
	h.jobMu.Lock()
	recs := make([]jobRecord, 0, len(h.jobs))
	for _, j := range h.jobs {
		recs = append(recs, j.snapshot())
	}
	h.jobMu.Unlock()
	slices.SortFunc(recs, func(a, b jobRecord) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), strings.Compare(a.ID, b.ID))
	})
	pages := max(1, (len(recs)+jobsPerPage-1)/jobsPerPage)
	start := min(len(recs), (page-1)*jobsPerPage)
	end := min(len(recs), start+jobsPerPage)
	writeJSON(w, jobList{Jobs: recs[start:end], Page: page, Pages: pages})
}

// heldNames returns the file names that the queued and running jobs write: each output file and each data package.
// The caller holds h.jobMu.
func (h *WebHandler) heldNames() map[string]bool {
	held := map[string]bool{}
	for _, j := range h.jobs {
		j.mu.Lock()
		rec := j.record
		j.mu.Unlock()
		if rec.State != jobQueued && rec.State != jobRunning {
			continue
		}
		held[rec.OutputFile] = true
		if pkg := j.req.packageOptions(); pkg != nil {
			held[datapackage.FileName(pkg.name)] = true
		}
	}
	return held
}

// holdsOutput reports whether held holds the output file outputFile or the data package of req.
func holdsOutput(held map[string]bool, outputFile string, req jobRequest) bool {
	return held[outputFile] || (req.DataPackage && held[datapackage.FileName(req.PackageName)])
}

// maxPackageName is the longest package name that datapackage.ValidName accepts.
const maxPackageName = 64

// uniqueOutput returns outputFile and the package name of req with the first suffix -2, -3 and so on that frees both names.
// A name is not free when a queued or running job writes a file of that name, the output file or the data package.
// The suffix shortens a long package name first, so the name stays valid by datapackage.ValidName.
// With no clash, it returns the names with no suffix. The caller holds h.jobMu.
func (h *WebHandler) uniqueOutput(outputFile string, req jobRequest) (string, string) {
	held := h.heldNames()
	ext := filepath.Ext(outputFile)
	base := strings.TrimSuffix(outputFile, ext)
	name, pkgName := outputFile, req.PackageName
	for n := 2; holdsOutput(held, name, jobRequest{DataPackage: req.DataPackage, PackageName: pkgName}); n++ {
		suffix := fmt.Sprintf("-%d", n)
		name = base + suffix + ext
		if req.DataPackage {
			// A valid package name holds only ASCII characters, so a byte cut is a character cut.
			pkgName = req.PackageName[:min(len(req.PackageName), maxPackageName-len(suffix))] + suffix
		}
	}
	return name, pkgName
}
