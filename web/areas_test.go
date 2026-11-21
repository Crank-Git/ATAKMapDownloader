package web

import (
	"regexp"
	"strings"
	"testing"
)

func TestIndexHoldsTheAreaToolbarAndTheAreaList(t *testing.T) {
	index := string(IndexHTML())
	tests := []struct {
		name string
		want []string
	}{
		{"the toolbar offers Rectangle, Polygon, Route and Import (FR-area-selection-19)", []string{
			`role="group" aria-label="Area tools"`,
			`id="drawRectangle"`, `> Rectangle</button>`,
			`id="drawPolygon"`, `> Polygon</button>`,
			`id="drawRoute"`, `> Route</button>`,
			`<label for="importFile" class="btn btn-sm">`, `> Import</label>`,
		}},
		{"the Route tool asks for a buffer with the default 500 (FR-area-selection-20)", []string{
			`<label for="routeBuffer">`,
			`id="routeBuffer" value="500" min="10" max="50000"`,
		}},
		{"Import takes the four file types by picker or by drop (FR-area-selection-21)", []string{
			`type="file" id="importFile" class="visually-hidden" accept=".geojson,.json,.kml,.kmz"`,
			`id="importDrop"`,
		}},
		{"the area list has an empty state and a total (FR-area-selection-22)", []string{
			`<ul id="areaList" class="area-list"></ul>`,
			`<p id="areaEmpty" class="hint">Draw or import an area</p>`,
			`id="areaTotal"`,
		}},
		{"the draw state tells the user how to cancel", []string{`id="drawHint"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range tt.want {
				if !strings.Contains(index, s) {
					t.Errorf("index.html does not contain %q", s)
				}
			}
		})
	}
	if strings.Contains(index, `id="selectionInfo"`) {
		t.Error("index.html still holds the one-rectangle selection card, which the area list replaces")
	}
}

func TestAppJSDrawsEachAreaKindWithGeoman(t *testing.T) {
	app := appJS(t)
	for _, want := range []string{
		"this.startDraw('Rectangle')",
		"this.startDraw('Polygon')",
		"this.startDraw('Line')",
		// Geoman leaves a draw mode open on Esc unless exitModeOnEscape is set.
		"exitModeOnEscape: true",
		"this.map.on('pm:drawstart'",
		"this.map.on('pm:drawend'",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js does not contain %q", want)
		}
	}
	create := methodBody(t, app, "onDrawCreate")
	for _, want := range []string{"Rectangle: 'bounds'", "Polygon: 'polygon'", "Line: 'route_buffer'", "this.routeBufferM()"} {
		if !strings.Contains(create, want) {
			t.Errorf("onDrawCreate does not contain %q", want)
		}
	}
}

func TestAppJSImportsThroughTheParseRouteOnly(t *testing.T) {
	app := appJS(t)
	imp := methodBody(t, app, "importAreaFile")
	for _, want := range []string{
		"'/api/areas/parse?'",
		"form.append('file', file, file.name)",
		// An absent default gives 0 on the server, so the request sends the sidebar defaults.
		"min_zoom:", "max_zoom:", "buffer_m:",
		"result.warnings",
		"this.showError('import'",
	} {
		if !strings.Contains(imp, want) {
			t.Errorf("importAreaFile does not contain %q", want)
		}
	}
	// The server holds the one parser, so app.js parses no XML and no zip (#53).
	for _, banned := range []string{"DOMParser", "DecompressionStream"} {
		if strings.Contains(app, banned) {
			t.Errorf("app.js contains %q, so it parses an area file itself", banned)
		}
	}
	if !strings.Contains(methodBody(t, app, "onAreaDrop"), "this.importAreaFile(") {
		t.Error("onAreaDrop does not import the dropped file")
	}
}

func TestAppJSSendsTheAreaListToEstimateAndStart(t *testing.T) {
	app := appJS(t)
	if strings.Contains(app, "selectedArea") {
		t.Error("app.js still reads selectedArea, which the area list replaces")
	}
	payload := methodBody(t, app, "areaPayload")
	for _, want := range []string{"kind:", "geometry:", "min_zoom:", "max_zoom:", "buffer_m", "name:"} {
		if !strings.Contains(payload, want) {
			t.Errorf("areaPayload does not contain %q (FR-area-selection-25)", want)
		}
	}
	for _, method := range []string{"estimateDownload", "startDownload"} {
		body := methodBody(t, app, method)
		if !strings.Contains(body, "areas: this.areaPayload()") {
			t.Errorf("%s does not send the area list", method)
		}
		if strings.Contains(body, "bounds: [") {
			t.Errorf("%s still sends the legacy bounds", method)
		}
	}
}

func TestAppJSShowsEachAreaInTheList(t *testing.T) {
	app := appJS(t)
	render := methodBody(t, app, "renderAreaList")
	for _, want := range []string{
		"KIND_LABEL[area.kind]",
		"name.textContent = area.name",
		"setAttribute('aria-label', 'Min zoom of ' + area.name)",
		"setAttribute('aria-label', 'Max zoom of ' + area.name)",
		"remove.textContent = 'Remove'",
		"setAttribute('aria-label', 'Remove ' + area.name)",
		"getElementById('areaEmpty').hidden = this.areas.length > 0",
		// A running job locks the zoom range and the Remove button of each row.
		"disabled = this.isDownloading",
	} {
		if !strings.Contains(render, want) {
			t.Errorf("renderAreaList does not contain %q (FR-area-selection-22, -23)", want)
		}
	}
	if !strings.Contains(methodBody(t, app, "setAreaZoom"), "this.areasChanged()") {
		t.Error("setAreaZoom does not count the tiles again after a zoom range change")
	}
	total := methodBody(t, app, "renderAreaTotal")
	for _, want := range []string{"this.uniqueTiles > this.maxTiles", "classList.toggle('over'", "this.showError('cap'"} {
		if !strings.Contains(total, want) {
			t.Errorf("renderAreaTotal does not contain %q", want)
		}
	}
	if !strings.Contains(methodBody(t, app, "loadConfig"), "this.maxTiles = config.max_tiles") {
		t.Error("loadConfig does not read the tile cap")
	}
}

func TestAppJSShadesTheSelectedAreaFromTheTilesRoute(t *testing.T) {
	shade := methodBody(t, appJS(t), "shadeSelectedArea")
	for _, want := range []string{
		"this.postJSON('/api/areas/tiles', { area: this.areaRequest(area), zoom: area.maxZoom })",
		// The route rejects 5,000 tiles or more, and the map then keeps the outline only (FR-area-selection-24).
		"error.code === 'too_many_tiles'",
		"pmIgnore: true",
	} {
		if !strings.Contains(shade, want) {
			t.Errorf("shadeSelectedArea does not contain %q", want)
		}
	}
}

func TestSetJobLockedStopsTheAreaTools(t *testing.T) {
	lock := methodBody(t, appJS(t), "setJobLocked")
	for _, want := range []string{
		"this.map.pm.disableDraw()",
		"this.map.pm.disableGlobalEditMode()",
		"this.map.pm.disableGlobalDragMode()",
		"this.map.pm.disableGlobalRemovalMode()",
		"this.map.pm.toggleControls()",
		"this.renderAreaList()",
	} {
		if !strings.Contains(lock, want) {
			t.Errorf("setJobLocked does not contain %q", want)
		}
	}
	if !regexp.MustCompile(`(?s)onAreaDrop\(e\) \{.*?if \(this\.isDownloading\) return`).MatchString(appJS(t)) {
		t.Error("onAreaDrop imports a file while a job runs")
	}
}
