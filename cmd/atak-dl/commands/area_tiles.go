package commands

import (
	"context"
	"fmt"
	"net/http"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// maxShadeTiles is the tile count at which the web UI stops the shading and shows the outline only (FR-area-selection-24).
const maxShadeTiles = 5000

// areaTilesResult is the POST /api/areas/tiles response. Each tile is [x, y] at the zoom level of the request.
type areaTilesResult struct {
	Tiles [][2]int `json:"tiles"`
}

// apiAreaTiles returns the tiles that one area selects at one zoom level, so the web UI can shade them.
// The body is {"area": <area in the shape of FR-area-selection-25>, "zoom": <level>}.
// The zoom range of the area is not used, because the web UI asks for one level only.
// It writes these errors:
//   - 400 invalid_area when the geometry does not decode or the area does not validate.
//   - 400 zoom_out_of_range when zoom is outside 0 to tilecalc.MaxZoomLevel.
//   - 413 too_many_tiles when the area selects maxShadeTiles tiles or more.
func (h *WebHandler) apiAreaTiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	var req struct {
		Area areaRequest `json:"area"`
		Zoom int         `json:"zoom"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Zoom < 0 || req.Zoom > tilecalc.MaxZoomLevel {
		writeError(w, http.StatusBadRequest, "zoom_out_of_range", fmt.Sprintf("zoom %d is outside the range 0-%d", req.Zoom, tilecalc.MaxZoomLevel))
		return
	}
	g, err := tilecalc.DecodeGeometry(req.Area.Geometry)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_area", err.Error())
		return
	}
	area := tilecalc.Area{Kind: req.Area.Kind, Geometry: g, BufferM: req.Area.BufferM}
	if err := area.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_area", err.Error())
		return
	}
	// The cancel stops the tile walk at the limit, so a large area costs no more than maxShadeTiles steps.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := areaTilesResult{Tiles: [][2]int{}}
	for tile := range area.Tiles(ctx, req.Zoom) {
		if len(out.Tiles) == maxShadeTiles-1 {
			cancel()
			writeError(w, http.StatusRequestEntityTooLarge, "too_many_tiles",
				fmt.Sprintf("the area selects %d tiles or more at zoom %d, so the map shows the outline only", maxShadeTiles, req.Zoom))
			return
		}
		out.Tiles = append(out.Tiles, tile)
	}
	writeJSON(w, out)
}
