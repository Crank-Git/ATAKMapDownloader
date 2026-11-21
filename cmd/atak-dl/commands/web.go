package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/policy"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/crank-git/atak-map-downloader/web"
)

// WebHandler serves the web interface and API.
type WebHandler struct {
	parser  *xmlparser.XMLParser
	calc    *tilecalc.TileCalculator
	mapsDir string
	// outputDir is the directory of the output files of job API jobs.
	outputDir string
	jobRoutes *http.ServeMux
	// userAgent is the User-Agent of every tile request and every geocode request (FR-polite-downloading-14).
	userAgent string
	// geocodeURL is the Nominatim search endpoint. A test points it at a fake Nominatim.
	geocodeURL string
	// policyFor returns the policy entry of a map source. A test gives a loopback tile server a high rate.
	policyFor func(*xmlparser.MapSource) policy.Entry
	// dlState is the job lock. createJob takes it, so two jobs never run at once.
	dlState struct {
		sync.Mutex
		downloading bool
		// job is the last job that the job API started. It stays after the job ends, so GET can read its result.
		job *job
	}
}

// NewWebHandler creates a new web handler.
// Every tile request and every geocode request of the handler sends userAgent.
func NewWebHandler(parser *xmlparser.XMLParser, calc *tilecalc.TileCalculator, mapsDir, userAgent string) *WebHandler {
	h := &WebHandler{
		parser:     parser,
		calc:       calc,
		mapsDir:    mapsDir,
		outputDir:  download.DownloadsDir(),
		jobRoutes:  http.NewServeMux(),
		userAgent:  userAgent,
		geocodeURL: "https://nominatim.openstreetmap.org/search",
		policyFor:  policy.ForSource,
	}
	h.routeJobs(h.jobRoutes)
	return h
}

// newServerHandler returns the handler that serve gives to the HTTP server.
// It rejects a cross-origin POST or DELETE with status 403 and the error code cross_origin
// (FR-security-hardening-2, FR-security-hardening-3).
// It passes a request with no Sec-Fetch-Site header and no Origin header, because curl and other clients send neither.
// Every request body goes through limitBody.
func newServerHandler(h http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusForbidden, "cross_origin", "Cross-origin request rejected")
	}))
	return cop.Handler(limitBody(h))
}

// maxBodyBytes is the largest request body that the server reads (FR-security-hardening-17).
const maxBodyBytes = 1 << 20

// limitBody returns a handler that rejects a request body larger than maxBodyBytes (FR-security-hardening-17).
// A body with a larger Content-Length gets 413 body_too_large at once.
// A body with no Content-Length gets the limit on read, and decodeJSON writes the 413.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBodyBytes {
			writeBodyTooLarge(w)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// writeBodyTooLarge writes the 413 body_too_large error response.
func writeBodyTooLarge(w http.ResponseWriter) {
	writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("Request body is larger than %d bytes", maxBodyBytes))
}

// ServeHTTP implements http.Handler.
func (h *WebHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		h.serveIndex(w, r)
	case r.URL.Path == "/api/jobs" || strings.HasPrefix(r.URL.Path, "/api/jobs/"):
		h.jobRoutes.ServeHTTP(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		h.serveAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/static/"):
		h.serveStatic(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "Route not found")
	}
}

func (h *WebHandler) serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(web.IndexHTML()) // A failed write means that the client closed the connection.
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
	case "/api/estimate":
		h.apiEstimate(w, r)
	case "/api/tile-info":
		h.apiTileInfo(w, r)
	case "/api/validate-source":
		h.apiValidateSource(w, r)
	case "/api/preview":
		h.apiPreview(w, r)
	case "/api/geocode":
		h.apiGeocode(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "Route not found")
	}
}

func (h *WebHandler) apiSources(w http.ResponseWriter, _ *http.Request) {
	sources := h.parser.DiscoverSources(false)
	list := []sourceItem{}
	for _, name := range h.parser.ListSources() {
		s := sources[name]
		p := policy.ForSource(s)
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
			PolicyHost:         p.HostPattern,
		})
	}
	writeJSON(w, list)
}

// sourceItem is one map source in the GET /api/sources response (FR-polite-downloading-16).
// PolicyHost is the host pattern of the policy entry, so the web UI can name the host that the rate applies to.
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
}

func (h *WebHandler) apiEstimate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var req struct {
		Bounds  []float64 `json:"bounds"`
		MinZoom int       `json:"min_zoom"`
		MaxZoom int       `json:"max_zoom"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Bounds) != 4 {
		writeError(w, http.StatusBadRequest, "invalid_bounds", "Bounds must hold four numbers: west, south, east, north")
		return
	}
	west, south, east, north := req.Bounds[0], req.Bounds[1], req.Bounds[2], req.Bounds[3]
	if req.MinZoom == 0 {
		req.MinZoom = 1
	}
	if req.MaxZoom == 0 {
		req.MaxZoom = 18
	}
	bounds, err := tilecalc.NewGeographicBounds(south, north, west, east)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_bounds", err.Error())
		return
	}
	if req.MinZoom > req.MaxZoom {
		req.MinZoom, req.MaxZoom = req.MaxZoom, req.MinZoom
	}
	var totalTiles int
	zoomBreakdown := make(map[int]int)
	for z := req.MinZoom; z <= req.MaxZoom; z++ {
		tb := h.calc.CalculateTileBounds(bounds, z)
		totalTiles += tb.TileCount()
		zoomBreakdown[z] = tb.TileCount()
	}
	estMB := float64(totalTiles*20) / 1024
	writeJSON(w, map[string]interface{}{
		"total_tiles":       totalTiles,
		"estimated_size_mb": estMB,
		"zoom_breakdown":    zoomBreakdown,
		"bounds":            req.Bounds,
		"zoom_range":        []int{req.MinZoom, req.MaxZoom},
	})
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
	source := h.parser.GetSource(req.SourceName)
	if source == nil {
		writeError(w, http.StatusNotFound, "unknown_source", "Source not found")
		return
	}
	errors := h.parser.ValidateSource(source)
	writeJSON(w, map[string]interface{}{
		"valid":       len(errors) == 0,
		"source_name": req.SourceName,
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
	source := h.parser.GetSource(req.Source)
	if source == nil {
		writeError(w, http.StatusNotFound, "unknown_source", "Source not found")
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
		"source":      req.Source,
		"total_tiles": len(tiles),
		"bounds":      req.Bounds,
	})
}

func (h *WebHandler) apiGeocode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(strings.TrimSpace(req.Query)) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_query", "Query is required")
		return
	}
	apiURL := h.geocodeURL + "?q=" + url.QueryEscape(req.Query) + "&format=json&limit=5&addressdetails=1"
	hr, err := http.NewRequestWithContext(r.Context(), http.MethodGet, apiURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Geocoding failed: "+err.Error())
		return
	}
	hr.Header.Set("User-Agent", h.userAgent)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(hr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Geocoding failed: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }() // The body is fully read, so a close error loses nothing.
	var results []struct {
		DisplayName string   `json:"display_name"`
		Lat         string   `json:"lat"`
		Lon         string   `json:"lon"`
		Boundingbox []string `json:"boundingbox"`
		Type        string   `json:"type"`
		Class       string   `json:"class"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Geocoding failed")
		return
	}
	var locations []map[string]interface{}
	for _, r := range results {
		// Nominatim sends lat and lon as JSON strings.
		lat, latErr := strconv.ParseFloat(r.Lat, 64)
		lon, lonErr := strconv.ParseFloat(r.Lon, 64)
		if latErr != nil || lonErr != nil {
			writeError(w, http.StatusInternalServerError, "internal", "Geocoding failed: the result has a coordinate that is not a number")
			return
		}
		locations = append(locations, map[string]interface{}{
			"display_name": r.DisplayName,
			"lat":          lat,
			"lon":          lon,
			"boundingbox":  r.Boundingbox,
			"type":         r.Type,
			"class":        r.Class,
		})
	}
	writeJSON(w, map[string]interface{}{
		"query":   req.Query,
		"results": locations,
		"count":   len(locations),
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
//   - 413 body_too_large when the body is larger than maxBodyBytes.
//   - 400 invalid_json when the body is not valid JSON.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	var tooLarge *http.MaxBytesError
	switch {
	case err == nil:
		return true
	case errors.As(err, &tooLarge):
		writeBodyTooLarge(w)
	default:
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body is not valid JSON")
	}
	return false
}
