package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/crank-git/atak-map-downloader/web"
	"github.com/gorilla/websocket"
)

// WebHandler serves the web interface and API.
type WebHandler struct {
	parser   *xmlparser.XMLParser
	calc     *tilecalc.TileCalculator
	mapsDir  string
	upgrader websocket.Upgrader
	dlState  struct {
		sync.Mutex
		downloading bool
		cancel      context.CancelFunc
	}
}

// NewWebHandler creates a new web handler.
func NewWebHandler(parser *xmlparser.XMLParser, calc *tilecalc.TileCalculator, mapsDir string) *WebHandler {
	return &WebHandler{
		parser:  parser,
		calc:    calc,
		mapsDir: mapsDir,
		upgrader: websocket.Upgrader{
			CheckOrigin:     func(_ *http.Request) bool { return true },
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
		},
	}
}

// ServeHTTP implements http.Handler.
func (h *WebHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Add CORS headers for all responses (avoids 403 on cross-origin requests)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch {
	case r.URL.Path == "/":
		h.serveIndex(w, r)
	case r.URL.Path == "/ws":
		h.serveWebSocket(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"):
		h.serveAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/static/"):
		h.serveStatic(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *WebHandler) serveIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(web.IndexHTML()) // A failed write means that the client closed the connection.
}

func (h *WebHandler) serveStatic(w http.ResponseWriter, r *http.Request) {
	fs, err := web.StaticFS()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	localPath := strings.TrimPrefix(r.URL.Path, "/static/")
	if localPath == "" {
		http.NotFound(w, r)
		return
	}
	f, err := fs.Open(localPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }() // The file is read-only, so a close error loses nothing.
	stat, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if stat.IsDir() {
		http.NotFound(w, r)
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
	list := []map[string]interface{}{}
	for _, name := range h.parser.ListSources() {
		s := sources[name]
		list = append(list, map[string]interface{}{
			"name":     name,
			"url":      s.URL,
			"min_zoom": s.MinZoom,
			"max_zoom": s.MaxZoom,
			"format":   s.TileType,
		})
	}
	writeJSON(w, list)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body is not valid JSON")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body is not valid JSON")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body is not valid JSON")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body is not valid JSON")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "Request body is not valid JSON")
		return
	}
	if len(strings.TrimSpace(req.Query)) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_query", "Query is required")
		return
	}
	apiURL := "https://nominatim.openstreetmap.org/search?q=" + url.QueryEscape(req.Query) + "&format=json&limit=5&addressdetails=1"
	hr, _ := http.NewRequest("GET", apiURL, nil)
	hr.Header.Set("User-Agent", "ATAK-Map-Downloader/1.0")
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
		var lat, lon float64
		// A value that does not parse stays 0. #17 adds the input validation.
		_, _ = fmt.Sscanf(r.Lat, "%f", &lat)
		_, _ = fmt.Sscanf(r.Lon, "%f", &lon)
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

func (h *WebHandler) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }() // The connection ends here, so a close error changes nothing.

	sendJSON(conn, map[string]interface{}{"type": "status", "message": "Connected to ATAK Downloader"})

	for {
		var msg struct {
			Type string                 `json:"type"`
			Data map[string]interface{} `json:"data"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			break
		}
		switch msg.Type {
		case "start_download":
			go h.handleStartDownload(conn, msg.Data)
		case "cancel_download":
			h.handleCancelDownload(conn)
		case "get_download_status":
			h.handleGetStatus(conn)
		}
	}
}

func (h *WebHandler) handleStartDownload(conn *websocket.Conn, data map[string]interface{}) {
	sourceName, _ := data["source"].(string)
	boundsRaw, _ := data["bounds"].([]interface{})
	if sourceName == "" || len(boundsRaw) != 4 {
		sendJSON(conn, map[string]interface{}{"type": "download_error", "message": "Missing required parameters"})
		return
	}
	source := h.parser.GetSource(sourceName)
	if source == nil {
		sendJSON(conn, map[string]interface{}{"type": "download_error", "message": "Source not found"})
		return
	}
	west := toFloat(boundsRaw[0])
	south := toFloat(boundsRaw[1])
	east := toFloat(boundsRaw[2])
	north := toFloat(boundsRaw[3])
	minZoom := int(toFloat(data["min_zoom"]))
	maxZoom := int(toFloat(data["max_zoom"]))
	if minZoom == 0 {
		minZoom = 1
	}
	if maxZoom == 0 {
		maxZoom = 15
	}
	bounds, err := tilecalc.NewGeographicBounds(south, north, west, east)
	if err != nil {
		sendJSON(conn, map[string]interface{}{"type": "download_error", "message": err.Error()})
		return
	}
	adv, _ := data["advanced_settings"].(map[string]interface{})
	cfg := downloadConfig(adv)

	customName, _ := data["custom_name"].(string)
	outputFilename := download.GenerateOutputFilename(sourceName, bounds, minZoom, maxZoom, customName)
	outputPath := filepath.Join(download.DownloadsDir(), outputFilename)
	zoomLevels := zoomLevelsBetween(minZoom, maxZoom)

	h.dlState.Lock()
	if h.dlState.downloading {
		h.dlState.Unlock()
		sendJSON(conn, map[string]interface{}{"type": "download_error", "message": "Download already in progress"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.dlState.cancel = cancel
	h.dlState.downloading = true
	h.dlState.Unlock()

	sendJSON(conn, map[string]interface{}{
		"type":        "download_started",
		"message":     fmt.Sprintf("Starting download of %s", sourceName),
		"output_file": outputFilename,
	})

	engine := download.NewEngine(cfg)
	// reportResult reads the result from success and ctx. #15 sends the error text to the client.
	success, _ := engine.DownloadTiles(ctx, source, bounds, zoomLevels, outputPath, func(p *download.Progress) bool {
		sendJSON(conn, map[string]interface{}{
			"type":       "download_progress",
			"progress":   p.ProgressPercent(),
			"downloaded": p.Downloaded,
			"total":      p.TotalTiles,
			"failed":     p.Failed,
			"rate":       p.TilesPerSecond(),
			"eta":        p.ETASeconds(),
			"message":    fmt.Sprintf("Downloaded %d/%d tiles (%.1f%%)", p.Downloaded, p.TotalTiles, p.ProgressPercent()),
		})
		return ctx.Err() == nil
	})

	h.dlState.Lock()
	h.dlState.downloading = false
	h.dlState.cancel = nil
	h.dlState.Unlock()

	reportResult(ctx, conn, success, outputFilename, outputPath)
}

// reportResult sends the last websocket message of a job.
func reportResult(ctx context.Context, conn *websocket.Conn, success bool, outputFilename, outputPath string) {
	if ctx.Err() != nil {
		// The engine removes the file on cancel, so this removal usually finds no file.
		_ = os.Remove(outputPath)
		sendJSON(conn, map[string]interface{}{"type": "download_cancelled", "message": "Download cancelled by user."})
		return
	}
	if success {
		sendJSON(conn, map[string]interface{}{
			"type":     "download_progress",
			"progress": 100, "message": "Download completed successfully!",
		})
		sendJSON(conn, map[string]interface{}{
			"type":     "download_complete",
			"message":  "Download complete! Saved to " + outputFilename,
			"filename": outputFilename,
			"path":     outputPath,
		})
	} else {
		sendJSON(conn, map[string]interface{}{
			"type":    "download_error",
			"message": "Download failed - too many tile errors. Try reducing concurrent downloads or enabling Ignore Errors.",
		})
	}
}

func (h *WebHandler) handleCancelDownload(conn *websocket.Conn) {
	h.dlState.Lock()
	if h.dlState.downloading && h.dlState.cancel != nil {
		h.dlState.cancel()
		h.dlState.Unlock()
		sendJSON(conn, map[string]interface{}{"type": "download_cancelled", "message": "Cancellation requested."})
	} else {
		h.dlState.Unlock()
		sendJSON(conn, map[string]interface{}{"type": "download_cancelled", "message": "No active download."})
	}
}

func (h *WebHandler) handleGetStatus(conn *websocket.Conn) {
	h.dlState.Lock()
	downloading := h.dlState.downloading
	h.dlState.Unlock()
	if downloading {
		sendJSON(conn, map[string]interface{}{"type": "status", "message": "Download in progress"})
	} else {
		sendJSON(conn, map[string]interface{}{"type": "status", "message": "No active download"})
	}
}

// downloadConfig returns the engine config for the advanced_settings of a start_download message.
// A setting that is missing or 0 gets the web UI default.
func downloadConfig(adv map[string]interface{}) download.Config {
	maxConcurrent := int(toFloat(adv["max_concurrent"]))
	if maxConcurrent == 0 {
		maxConcurrent = 5
	}
	delay := toFloat(adv["delay_between_batches"])
	if delay == 0 {
		delay = 0.2
	}
	failureTol := toFloat(adv["failure_tolerance"])
	if failureTol == 0 {
		failureTol = 0.3
	}
	ignoreErrors, _ := adv["ignore_errors"].(bool)
	verifySSL, _ := adv["verify_ssl"].(bool)

	cfg := download.DefaultConfig()
	cfg.MaxConcurrent = maxConcurrent
	cfg.DelayBetweenBatches = time.Duration(delay * float64(time.Second))
	cfg.MaxFailureRate = failureTol
	cfg.IgnoreErrors = ignoreErrors
	cfg.VerifySSL = verifySSL
	return cfg
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

// sendJSON sends v as one websocket message.
// A failed write means that the client left, so the error is not used. #15 replaces the websocket.
func sendJSON(conn *websocket.Conn, v interface{}) {
	_ = conn.WriteJSON(v)
}

func toFloat(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case string:
		var f float64
		// A value that does not parse stays 0. #17 adds the input validation.
		_, _ = fmt.Sscanf(x, "%f", &f)
		return f
	}
	return 0
}
