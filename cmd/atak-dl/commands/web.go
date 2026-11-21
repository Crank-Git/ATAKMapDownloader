package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/jobs"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/crank-git/atak-map-downloader/web"
	"golang.org/x/time/rate"
)

// WebHandler serves the web interface and API.
type WebHandler struct {
	parser  *xmlparser.XMLParser
	calc    *tilecalc.TileCalculator
	mapsDir string
	// outputDir is the directory of the output files of job API jobs.
	outputDir string
	// maxTiles is the tile cap of a job API job (FR-robust-downloads-27).
	maxTiles  int
	jobRoutes *http.ServeMux
	// userAgent is the User-Agent of every tile request and every geocode request (FR-polite-downloading-14).
	userAgent string
	// version is the version of the binary that GET /healthz reports (FR-team-server-29).
	version string
	// geocodeURL is the Nominatim search endpoint. A test points it at a fake Nominatim.
	geocodeURL string
	// geocodeOff removes the /api/geocode route (FR-polite-downloading-26).
	geocodeOff bool
	// geocodeClient is shared, so the geocode requests reuse one connection pool.
	geocodeClient *http.Client
	// geocodeLimiter is one limiter for every client of the server (FR-polite-downloading-22).
	geocodeLimiter *rate.Limiter
	// geocodeWait is the longest wait for the limiter. A test makes it short.
	geocodeWait  time.Duration
	geocodeCache *geocodeCache
	// auth is the token check of FR-team-server-17. It is nil on loopback.
	auth *tokenAuth
	// queue holds the queued jobs and starts them on its workers (FR-team-server-1, -2, -3).
	queue *jobs.Queue
	// jobMu guards jobs.
	jobMu sync.Mutex
	// jobs holds every job of the server by id. A job stays after it ends, so GET can read its result.
	jobs map[string]*job
	// store is the jobs.jsonl file of the data directory. useDataDir opens it.
	store *jobs.Store
	// addr and workers are the --addr and --workers values of serve. The footer of the Jobs page shows them.
	addr    string
	workers int
}

// NewWebHandler creates a new web handler.
// Every tile request and every geocode request of the handler sends userAgent.
func NewWebHandler(parser *xmlparser.XMLParser, calc *tilecalc.TileCalculator, mapsDir, userAgent string) *WebHandler {
	h := &WebHandler{
		parser:     parser,
		calc:       calc,
		mapsDir:    mapsDir,
		outputDir:  download.DownloadsDir(),
		maxTiles:   defaultMaxTiles,
		jobRoutes:  http.NewServeMux(),
		userAgent:  userAgent,
		geocodeURL: defaultGeocoderURL + "/search",
		// The Nominatim usage policy allows at most 1 request per second (https://operations.osmfoundation.org/policies/nominatim/).
		geocodeLimiter: rate.NewLimiter(1, 1),
		geocodeClient:  &http.Client{Timeout: 10 * time.Second},
		geocodeWait:    geocodeWait,
		geocodeCache:   newGeocodeCache(),
		jobs:           map[string]*job{},
	}
	h.queue = jobs.NewQueue(h.startJob)
	h.routeJobs(h.jobRoutes)
	return h
}

// newServerHandler returns the handler that serve gives to the HTTP server.
// It rejects a cross-origin POST or DELETE with status 403 and the error code cross_origin
// (FR-security-hardening-2, FR-security-hardening-3).
// It passes a request with no Sec-Fetch-Site header and no Origin header, because curl and other clients send neither.
// Every request without the token of h.auth gets the response of tokenAuth.handler.
// Every request body goes through limitBody.
func newServerHandler(h *WebHandler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusForbidden, "cross_origin", "Cross-origin request rejected")
	}))
	return cop.Handler(h.auth.handler(limitBody(h)))
}

// maxBodyBytes is the largest request body that the server reads (FR-security-hardening-17).
const maxBodyBytes = 1 << 20

// areaRoutes are the paths whose request body carries areas.
var areaRoutes = map[string]bool{"/api/areas/parse": true, "/api/areas/tiles": true, "/api/estimate": true, "/api/jobs": true}

// limitBody returns a handler that rejects a request body larger than maxBodyBytes (FR-security-hardening-17).
// A route in areaRoutes has the larger limit areaBodyBytes, because its areas can come from a 5 MiB file.
// A body with a larger Content-Length gets 413 body_too_large at once.
// A body with no Content-Length gets the limit on read, and the handler writes the 413.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(maxBodyBytes)
		if areaRoutes[r.URL.Path] {
			limit = areaBodyBytes
		}
		if r.ContentLength > limit {
			writeBodyTooLarge(w, limit)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// writeBodyTooLarge writes the 413 body_too_large error response for a body above limit bytes.
func writeBodyTooLarge(w http.ResponseWriter, limit int64) {
	writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("Request body is larger than %d bytes", limit))
}

// ServeHTTP implements http.Handler.
func (h *WebHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		h.serveIndex(w, r)
	case r.URL.Path == "/healthz":
		h.serveHealth(w, r)
	case r.URL.Path == "/jobs":
		servePage(w, web.JobsHTML())
	case r.URL.Path == "/signin":
		servePage(w, web.SigninHTML())
	case r.URL.Path == "/api/jobs" || strings.HasPrefix(r.URL.Path, "/api/jobs/"):
		h.jobRoutes.ServeHTTP(w, r)
	case r.URL.Path == "/api/session":
		h.apiSession(w, r)
	case r.URL.Path == "/api/files" || strings.HasPrefix(r.URL.Path, "/api/files/"):
		h.serveFiles(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		h.serveAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/static/"):
		h.serveStatic(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "Route not found")
	}
}

func (h *WebHandler) serveIndex(w http.ResponseWriter, _ *http.Request) {
	servePage(w, web.IndexHTML())
}

// servePage writes the HTML page.
func servePage(w http.ResponseWriter, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page) // A failed write means that the client closed the connection.
}

func (h *WebHandler) serveStatic(w http.ResponseWriter, r *http.Request) {
	fs, err := web.StaticFS()
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "File not found")
		return
	}
	localPath := strings.TrimPrefix(r.URL.Path, "/static/")
	if localPath == "" {
		writeError(w, http.StatusNotFound, "not_found", "File not found")
		return
	}
	f, err := fs.Open(localPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "File not found")
		return
	}
	defer func() { _ = f.Close() }() // The file is read-only, so a close error loses nothing.
	stat, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "File not found")
		return
	}
	if stat.IsDir() {
		writeError(w, http.StatusNotFound, "not_found", "File not found")
		return
	}
	// Set content type for common files
	if strings.HasSuffix(localPath, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else if strings.HasSuffix(localPath, ".js") {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	}
	_, _ = io.Copy(w, f) // A failed copy means that the client closed the connection.
}

func (h *WebHandler) serveAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/sources":
		h.apiSources(w, r)
	case "/api/sources/skipped":
		h.apiSkippedSources(w, r)
	case "/api/estimate":
		h.apiEstimate(w, r)
	case "/api/areas/parse":
		h.apiAreasParse(w, r)
	case "/api/areas/tiles":
		h.apiAreaTiles(w, r)
	case "/api/tile-info":
		h.apiTileInfo(w, r)
	case "/api/validate-source":
		h.apiValidateSource(w, r)
	case "/api/preview":
		h.apiPreview(w, r)
	case "/api/geocode":
		if h.geocodeOff {
			writeError(w, http.StatusNotFound, "not_found", "Route not found")
			return
		}
		h.apiGeocode(w, r)
	case "/api/config":
		h.apiConfig(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "Route not found")
	}
}

func (h *WebHandler) apiSources(w http.ResponseWriter, _ *http.Request) {
	sources := h.parser.DiscoverSources()
	list := []sourceItem{}
	for _, name := range h.parser.ListSources() {
		s := sources[name]
		p := sourcePolicy(s)
		list = append(list, sourceItem{
			Name:               name,
			URL:                s.URL,
			MinZoom:            s.MinZoom,
			MaxZoom:            s.MaxZoom,
			Format:             s.TileType,
			PolicyClass:        string(p.PolicyClass),
			PolicyReason:       p.Reason,
			PolicyReferenceURL: p.ReferenceURL,
			RequestsPerSecond:  p.RequestsPerSecond,
			PolicyHost:         p.Hosts(),
			Kind:               s.Kind,
			Origin:             s.Origin,
		})
	}
	writeJSON(w, list)
}

func (h *WebHandler) apiSkippedSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	list := []skippedItem{}
	for _, s := range h.parser.SkippedSources() {
		list = append(list, skippedItem{Path: s.Path, Reason: s.Reason, Detail: s.Detail})
	}
	writeJSON(w, list)
}

// skippedItem is one source file in the GET /api/sources/skipped response (FR-source-sync-29).
type skippedItem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// sourceItem is one map source in the GET /api/sources response (FR-polite-downloading-16).
// PolicyHost lists the host patterns of the policy entry, so the web UI can name the hosts that the rate applies to.
type sourceItem struct {
	Name               string  `json:"name"`
	URL                string  `json:"url"`
	MinZoom            int     `json:"min_zoom"`
	MaxZoom            int     `json:"max_zoom"`
	Format             string  `json:"format"`
	PolicyClass        string  `json:"policy_class"`
	PolicyReason       string  `json:"policy_reason"`
	PolicyReferenceURL string  `json:"policy_reference_url"`
	RequestsPerSecond  float64 `json:"requests_per_second"`
	PolicyHost         string  `json:"policy_host"`
	Kind               string  `json:"kind"`
	Origin             string  `json:"origin"`
}

// apiEstimate returns the tile count and the size of a job (feature 02).
// The request holds an area list, or the legacy bounds and zoom range of one bounds area (FR-area-selection-25, -26).
// The response counts each area and the unique tiles over all areas, and total_tiles is the unique count (FR-area-selection-9, -10).
func (h *WebHandler) apiEstimate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var req struct {
		// Source is optional. With no source, the estimate uses the size per tile of an unknown tile type.
		Source  string        `json:"source"`
		Areas   []areaRequest `json:"areas"`
		Bounds  []float64     `json:"bounds"`
		MinZoom int           `json:"min_zoom"`
		MaxZoom int           `json:"max_zoom"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	// The legacy request has zoom defaults and accepts an inverted zoom range, as feature 02 defines it.
	if req.MinZoom == 0 {
		req.MinZoom = 1
	}
	if req.MaxZoom == 0 {
		req.MaxZoom = 18
	}
	if req.MinZoom > req.MaxZoom {
		req.MinZoom, req.MaxZoom = req.MaxZoom, req.MinZoom
	}
	areas, code, message := requestAreas(req.Bounds, req.MinZoom, req.MaxZoom, req.Areas)
	if code == "" {
		code, message = checkAreas(areas)
	}
	if code != "" {
		writeError(w, http.StatusBadRequest, code, message)
		return
	}
	tileType := ""
	if req.Source != "" {
		source := h.parser.GetSource(req.Source)
		if source == nil {
			writeError(w, http.StatusNotFound, "unknown_source", "Source not found")
			return
		}
		tileType = source.TileType
	}
	count := tilecalc.CountJobTiles(areas)
	perArea := make([]areaEstimate, len(areas))
	for i, a := range areas {
		perArea[i] = areaEstimate{Name: a.Name, Tiles: count.Areas[i]}
	}
	b := tilecalc.JobBound(areas)
	minZoom, maxZoom := tilecalc.JobZoomRange(areas)
	writeJSON(w, estimateResult{
		TotalTiles:      count.Unique,
		EstimatedSizeMB: float64(count.Unique) * tilecalc.TileSizeKB(tileType) / 1024,
		ZoomBreakdown:   count.Zooms,
		Bounds:          []float64{b.Min[0], b.Min[1], b.Max[0], b.Max[1]},
		ZoomRange:       []int{minZoom, maxZoom},
		Areas:           perArea,
		UniqueTiles:     count.Unique,
	})
}

// estimateResult is the POST /api/estimate response.
// ZoomBreakdown maps each zoom level to its count of unique tiles. TotalTiles and UniqueTiles hold the same count.
type estimateResult struct {
	TotalTiles      int            `json:"total_tiles"`
	EstimatedSizeMB float64        `json:"estimated_size_mb"`
	ZoomBreakdown   map[int]int    `json:"zoom_breakdown"`
	Bounds          []float64      `json:"bounds"`
	ZoomRange       []int          `json:"zoom_range"`
	Areas           []areaEstimate `json:"areas"`
	UniqueTiles     int            `json:"unique_tiles"`
}

// areaEstimate is the tile count of one area alone in the POST /api/estimate response.
type areaEstimate struct {
	Name  string `json:"name"`
	Tiles int    `json:"tiles"`
}

func (h *WebHandler) apiTileInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var req struct {
		Lat  float64 `json:"lat"`
		Lon  float64 `json:"lon"`
		Zoom int     `json:"zoom"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Zoom == 0 {
		req.Zoom = 10
	}
	tileX, tileY := h.calc.LatLonToTile(req.Lat, req.Lon, req.Zoom)
	topLat, leftLon := h.calc.TileToLatLon(tileX, tileY, req.Zoom)
	botLat, rightLon := h.calc.TileToLatLon(tileX+1, tileY+1, req.Zoom)
	writeJSON(w, map[string]interface{}{
		"tile_x":     tileX,
		"tile_y":     tileY,
		"zoom":       req.Zoom,
		"bounds":     []float64{leftLon, botLat, rightLon, topLat},
		"coordinate": []float64{req.Lat, req.Lon},
	})
}

func (h *WebHandler) apiValidateSource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var req struct {
		SourceName string `json:"source_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	source, ok := h.findWebSource(w, req.SourceName)
	if !ok {
		return
	}
	errors := h.parser.ValidateSource(source)
	writeJSON(w, map[string]interface{}{
		"valid":       len(errors) == 0,
		"source_name": source.Name,
		"url":         source.URL,
		"zoom_range":  []int{source.MinZoom, source.MaxZoom},
		"format":      source.TileType,
		"issues":      errors,
	})
}

func (h *WebHandler) apiPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var req struct {
		Source  string    `json:"source"`
		Bounds  []float64 `json:"bounds"`
		MinZoom int       `json:"min_zoom"`
		MaxZoom int       `json:"max_zoom"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Source == "" {
		writeError(w, http.StatusBadRequest, "unknown_source", "Source name is required")
		return
	}
	if len(req.Bounds) != 4 {
		writeError(w, http.StatusBadRequest, "invalid_bounds", "Bounds must hold four numbers: west, south, east, north")
		return
	}
	source, ok := h.findWebSource(w, req.Source)
	if !ok {
		return
	}
	if req.MinZoom == 0 {
		req.MinZoom = 1
	}
	if req.MaxZoom == 0 {
		req.MaxZoom = 18
	}
	west, south, east, north := req.Bounds[0], req.Bounds[1], req.Bounds[2], req.Bounds[3]
	bounds, err := tilecalc.NewGeographicBounds(south, north, west, east)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_bounds", err.Error())
		return
	}
	previewZoom := req.MaxZoom
	tb := h.calc.CalculateTileBounds(bounds, previewZoom)
	maxPerSide := 4
	if previewZoom <= 10 {
		maxPerSide = 6
	} else if previewZoom > 15 {
		maxPerSide = 3
	}
	wx := min(tb.MaxX-tb.MinX+1, maxPerSide)
	wy := min(tb.MaxY-tb.MinY+1, maxPerSide)
	cx := (tb.MinX + tb.MaxX) / 2
	cy := (tb.MinY + tb.MaxY) / 2
	startX := max(tb.MinX, cx-wx/2)
	startY := max(tb.MinY, cy-wy/2)
	endX := min(tb.MaxX, startX+wx-1)
	endY := min(tb.MaxY, startY+wy-1)
	var tiles []map[string]interface{}
	for x := startX; x <= endX; x++ {
		for y := startY; y <= endY; y++ {
			north, west := h.calc.TileToLatLon(x, y, previewZoom)
			south, east := h.calc.TileToLatLon(x+1, y+1, previewZoom)
			tiles = append(tiles, map[string]interface{}{
				"url":    source.FormatURL(x, y, previewZoom, ""),
				"x":      x,
				"y":      y,
				"zoom":   previewZoom,
				"bounds": []float64{west, south, east, north},
			})
		}
	}
	writeJSON(w, map[string]interface{}{
		"tiles":       tiles,
		"zoom":        previewZoom,
		"source":      source.Name,
		"total_tiles": len(tiles),
		"bounds":      req.Bounds,
	})
}

// writeJSON encodes v as a JSON response body with status 200.
// A failed write means that the client closed the connection, so the error is not used.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// apiError is the body of every error response from the /api/ routes.
// It encodes as {"error":{"code":"<code>","message":"<text>"}} (FR-security-hardening-22).
type apiError struct {
	Error apiErrorDetail `json:"error"`
}

// apiErrorDetail holds a code that a client can match and a message for a person.
type apiErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError writes an error response with the given status, error code and message.
// The code is one of the error codes in docs/specs/features/05-security-hardening.md.
// A failed write means that the client closed the connection, so the error is not used.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Error: apiErrorDetail{Code: code, Message: message}})
}

// decodeJSON decodes the request body into v.
// It returns false when it wrote an error response:
//   - 413 body_too_large when the body is larger than the limit of limitBody.
//   - 400 invalid_json when the body is not valid JSON.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		writeBodyTooLarge(w, tooLarge.Limit)
	default:
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body is not valid JSON")
	}
	return false
}
