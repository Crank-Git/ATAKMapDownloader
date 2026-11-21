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
	"github.com/crank-git/atak-map-downloader/internal/output"
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

// maxTileZoom is the highest zoom level that the tile route accepts, so that 1<<z fits in an int on every platform.
const maxTileZoom = 30

// serveFiles serves GET /api/files, GET /api/files/{name} and GET /api/files/{name}/tiles/{z}/{x}/{y}.
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
	rest := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if parts := strings.Split(rest, "/"); len(parts) == 5 && parts[1] == "tiles" {
		h.serveTile(w, parts[0], parts[2:])
		return
	}
	h.downloadFile(w, r, rest)
}

// serveTile writes one tile of the output file name (FR-offline-web-ui-13).
// zxy holds the zoom level, the column and the XYZ row of the request.
// It writes:
//   - 204 when the output file does not hold the tile (FR-offline-web-ui-14).
//   - 404 not_found when zxy is not a tile at its zoom level.
//   - 404 file_not_found when name is not an MBTiles output file in the output directory, for example a data package.
func (h *WebHandler) serveTile(w http.ResponseWriter, name string, zxy []string) {
	notFound := func() { writeError(w, http.StatusNotFound, "file_not_found", "Output file not found") }
	if !validOutputFileName(name) {
		notFound()
		return
	}
	var c [3]int
	for i, s := range zxy {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 {
			writeError(w, http.StatusNotFound, "not_found", "Tile not found")
			return
		}
		c[i] = v
	}
	z, x, y := c[0], c[1], c[2]
	if z > maxTileZoom || x >= 1<<z || y >= 1<<z {
		writeError(w, http.StatusNotFound, "not_found", "Tile not found")
		return
	}

	root, err := os.OpenRoot(h.outputDir)
	if err != nil {
		notFound()
		return
	}
	defer func() { _ = root.Close() }() // The root is read-only here, so a close error loses nothing.
	// The check stops the driver from creating a missing file.
	f, _, ok := openOutputFile(root, name)
	if !ok {
		notFound()
		return
	}
	defer func() { _ = f.Close() }() // The file is read-only here, so a close error loses nothing.
	// SQLite opens a file by path, so ReadTile gets the path of the file that openOutputFile checked.
	data, ok, err := mbtiles.ReadTile(filepath.Join(h.outputDir, name), z, x, y)
	if err != nil {
		slog.Debug("reject a tile request for a file that is not an MBTiles output file", "name", name, "error", err)
		notFound()
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The engine stores only a PNG, JPEG, GIF or WebP body, and DetectContentType knows each signature.
	contentType := http.DetectContentType(data)
	// A user can copy any file into the output directory, so a body that is not an image goes out as octet-stream.
	if !strings.HasPrefix(contentType, "image/") {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	//nolint:gosec // G705: The Content-Type is an image type or application/octet-stream with nosniff, so the browser runs no script from the body.
	_, _ = w.Write(data) // A failed write means that the client closed the connection.
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
// An entry is an output file when it is a regular file with an allowed name that output.ReadInfo accepts.
// The schema of a running job can be absent for a moment after the engine creates the file, so inProgress keeps that file.
func (h *WebHandler) outputFile(root *os.Root, name string, inProgress bool) (outputFileItem, bool) {
	if !validOutputFileName(name) {
		return outputFileItem{}, false
	}
	f, info, ok := openOutputFile(root, name)
	if !ok {
		return outputFileItem{}, false
	}
	defer func() { _ = f.Close() }() // The file is read-only here, so a close error loses nothing.
	item := outputFileItem{Name: name, Size: info.Size(), Modified: info.ModTime(), InProgress: inProgress}

	// SQLite opens a file by path, so ReadInfo gets the path of the file that openOutputFile checked.
	meta, err := output.ReadInfo(filepath.Join(h.outputDir, name))
	if err != nil {
		if !inProgress {
			slog.Debug("omit a file that is not an output file", "name", name, "error", err)
			return outputFileItem{}, false
		}
		return item, true
	}
	item.Format = meta.TileFormat
	item.Source = meta.Source
	item.MinZoom = meta.MinZoom
	item.MaxZoom = meta.MaxZoom
	item.Bounds = meta.Bounds
	return item, true
}

// openOutputFile opens the entry name in root and returns the file and its FileInfo.
// It returns false when the entry is missing, is not a regular file, or is a symbolic link.
// The checks read the open handle, so the FileInfo describes the file that the download sends.
// The Lstat after the open rejects a link inside the root, and os.SameFile rejects an entry that changed after the open.
// SQLite opens a file by path, so a swap after these checks can still reach the tile and metadata reads.
func openOutputFile(root *os.Root, name string) (*os.File, fs.FileInfo, bool) {
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, false
	}
	info, err := f.Stat()
	if err == nil && info.Mode().IsRegular() {
		entry, lerr := root.Lstat(name)
		if lerr == nil && os.SameFile(info, entry) {
			return f, info, true
		}
	}
	_ = f.Close() // The file is read-only here, so a close error loses nothing.
	return nil, nil, false
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
	f, info, ok := openOutputFile(root, name)
	if !ok {
		notFound()
		return
	}
	defer func() { _ = f.Close() }() // The file is read-only here, so a close error loses nothing.
	// The name holds only [A-Za-z0-9._-], so it needs no escape inside the quotes.
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
