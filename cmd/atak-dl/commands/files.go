package commands

import (
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
)

// outputFileItem is one output file in the GET /api/files response (FR-offline-web-ui-7).
// Bounds is [west, south, east, north], or null when the metadata has no valid bounds row.
type outputFileItem struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Format   string    `json:"format"`
	Source   string    `json:"source"`
	MinZoom  int       `json:"min_zoom"`
	MaxZoom  int       `json:"max_zoom"`
	Bounds   []float64 `json:"bounds"`
	// InProgress is true while a job writes the file, so the web UI disables its Download button.
	InProgress bool `json:"in_progress"`
}

// outputFileName matches the names that the file routes accept.
// The pattern has no "/", so a name never holds a path.
var outputFileName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validOutputFileName reports whether name is an allowed output file name.
// The pattern accepts "." and "..", so the check rejects them by name.
func validOutputFileName(name string) bool {
	return outputFileName.MatchString(name) && name != "." && name != ".."
}

// serveFiles serves GET /api/files and GET /api/files/{name}.
// It reads r.URL.Path itself, because http.ServeMux redirects a path with ".." before a handler can return file_not_found.
func (h *WebHandler) serveFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	if r.URL.Path == "/api/files" {
		h.listFiles(w)
		return
	}
	h.downloadFile(w, r, strings.TrimPrefix(r.URL.Path, "/api/files/"))
}

// listFiles writes the output files in the output directory, newest first (FR-offline-web-ui-7).
// It writes an empty list when the output directory does not exist, because no job has run yet.
// It writes 500 internal when it cannot read the output directory.
func (h *WebHandler) listFiles(w http.ResponseWriter) {
	root, err := os.OpenRoot(h.outputDir)
	if errors.Is(err, fs.ErrNotExist) {
		writeJSON(w, []outputFileItem{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	defer func() { _ = root.Close() }() // The root is read-only here, so a close error loses nothing.
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	running := h.runningOutputFile()
	items := []outputFileItem{}
	for _, e := range entries {
		item, ok := h.outputFile(root, e.Name(), e.Name() == running)
		if ok {
			items = append(items, item)
		}
	}
	slices.SortFunc(items, func(a, b outputFileItem) int {
		if c := b.Modified.Compare(a.Modified); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	writeJSON(w, items)
}

// runningOutputFile returns the output file name of the running job, or "" when no job runs.
func (h *WebHandler) runningOutputFile() string {
	h.dlState.Lock()
	defer h.dlState.Unlock()
	if !h.dlState.downloading {
		return ""
	}
	return h.dlState.job.snapshot().OutputFile
}

// outputFile returns the list item of the entry name in root, and false when the entry is not an output file.
// An entry is an output file when it is a regular file with an allowed name and a readable metadata table.
// The metadata of a running job can be absent for a moment after the engine creates the file, so inProgress keeps that file.
func (h *WebHandler) outputFile(root *os.Root, name string, inProgress bool) (outputFileItem, bool) {
	if !validOutputFileName(name) {
		return outputFileItem{}, false
	}
	// Lstat does not follow a symbolic link, so the metadata read below opens only a file inside the root.
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return outputFileItem{}, false
	}
	item := outputFileItem{Name: name, Size: info.Size(), Modified: info.ModTime(), InProgress: inProgress}

	// SQLite opens a file by path, so ReadMetadata gets the path of the file that Lstat checked.
	path := filepath.Join(h.outputDir, name)
	meta := map[string]string{}
	for _, key := range []string{"format", "atakdl_source", "minzoom", "maxzoom", "bounds"} {
		value, _, err := mbtiles.ReadMetadata(path, key)
		if err != nil {
			if !inProgress {
				slog.Debug("omit a file that is not an output file", "name", name, "error", err)
				return outputFileItem{}, false
			}
			break
		}
		meta[key] = value
	}
	item.Format = meta["format"]
	item.Source = meta["atakdl_source"]
	// A v0.1 output file can lack a row, so a missing or bad zoom row gives 0.
	item.MinZoom, _ = strconv.Atoi(meta["minzoom"])
	item.MaxZoom, _ = strconv.Atoi(meta["maxzoom"])
	item.Bounds = parseBounds(meta["bounds"])
	return item, true
}

// parseBounds returns the four numbers of an MBTiles bounds row "west,south,east,north".
// It returns nil when the row does not hold four numbers.
func parseBounds(row string) []float64 {
	parts := strings.Split(row, ",")
	if len(parts) != 4 {
		return nil
	}
	bounds := make([]float64, 4)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil
		}
		bounds[i] = v
	}
	return bounds
}

// downloadFile writes the output file name as an attachment (FR-offline-web-ui-8).
// It opens the file through os.Root on the output directory, so no name reaches a file outside it (FR-offline-web-ui-9).
// It writes 404 file_not_found when name is not a regular file in the output directory (FR-offline-web-ui-10).
func (h *WebHandler) downloadFile(w http.ResponseWriter, r *http.Request, name string) {
	notFound := func() { writeError(w, http.StatusNotFound, "file_not_found", "Output file not found") }
	if !validOutputFileName(name) {
		notFound()
		return
	}
	root, err := os.OpenRoot(h.outputDir)
	if err != nil {
		notFound()
		return
	}
	defer func() { _ = root.Close() }() // The root is read-only here, so a close error loses nothing.
	f, err := root.Open(name)
	if err != nil {
		notFound()
		return
	}
	defer func() { _ = f.Close() }() // The file is read-only here, so a close error loses nothing.
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		notFound()
		return
	}
	// The name holds only [A-Za-z0-9._-], so it needs no escape inside the quotes.
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
