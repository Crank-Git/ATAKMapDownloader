package commands

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// captureWarnings sends the default logger to a buffer until the test ends.
// No test in this package calls t.Parallel, so the global logger change reaches no other test.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestValidateSourceAndPreviewAcceptAnOldSourceName(t *testing.T) {
	xml := strings.Replace(testSourceXML, "Test Source", "USGS Topo", 1)
	tests := []struct {
		name string
		path string
		body string
		// field is the response field that names the map source.
		field string
	}{
		{"validate-source", "/api/validate-source", `{"source_name":"USGS - Usgsbasemap"}`, "source_name"},
		{"preview", "/api/preview", `{"source":"USGS - Usgsbasemap","bounds":[0,0,1,1],"max_zoom":3}`, "source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureWarnings(t)
			h := newTestHandler(t, map[string]string{"topo.xml": xml})

			rec := serve(h, http.MethodPost, tt.path, tt.body)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body[tt.field] != "USGS Topo" {
				t.Errorf("%s = %v, want the v2 name \"USGS Topo\"", tt.field, body[tt.field])
			}
			want := `is an old map source name. The new name is \"USGS Topo\".`
			if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), want) {
				t.Errorf("server log = %q, want a warning with %q", logs.String(), want)
			}
		})
	}
}

func TestValidateSourceAndPreviewRejectASourceThatUpstreamRemoved(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{"validate-source", "/api/validate-source", `{"source_name":"Michelin - OSM Michelin"}`},
		{"preview", "/api/preview", `{"source":"Michelin - OSM Michelin","bounds":[0,0,1,1],"max_zoom":3}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})

			rec := serve(h, http.MethodPost, tt.path, tt.body)

			if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "map source removed upstream in ATAK-Maps v1.10.1") {
				t.Errorf("response = %d %s, want 404 with the removed version", rec.Code, rec.Body)
			}
		})
	}
}

func TestEstimateResolvesAnOldSourceName(t *testing.T) {
	// The jpg tile type has a size per tile that differs from the size of an unknown tile type.
	xml := strings.NewReplacer("Test Source", "Google Satellite", "<tileType>png</tileType>", "<tileType>jpg</tileType>").Replace(testSourceXML)
	h := newTestHandler(t, map[string]string{"satellite.xml": xml})
	const area = `"bounds":[0,0,1,1],"min_zoom":1,"max_zoom":5`
	v2 := serve(h, http.MethodPost, "/api/estimate", `{"source":"Google Satellite",`+area+`}`)
	if v2.Code != http.StatusOK {
		t.Fatalf("v2 name: status = %d, want 200; body %s", v2.Code, v2.Body)
	}
	var want estimateResult
	if err := json.Unmarshal(v2.Body.Bytes(), &want); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name        string
		source      string
		wantCode    int
		wantWarning string
	}{
		{"returns the v2 estimate and a warning for a v0.1 name", "Google - Satellite Only", http.StatusOK, `"Google - Satellite Only" is an old map source name. The new name is "Google Satellite".`},
		{"returns no warning for a v2 name", "Google Satellite", http.StatusOK, "<absent>"},
		{"returns 404 for an unknown name", "No Such Source", http.StatusNotFound, "<absent>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, http.MethodPost, "/api/estimate", `{"source":"`+tt.source+`",`+area+`}`)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantCode, rec.Body)
			}
			if tt.wantCode != http.StatusOK {
				if !strings.Contains(rec.Body.String(), `"unknown_source"`) {
					t.Errorf("body = %s, want the unknown_source code", rec.Body)
				}
				return
			}
			got, warning := decodeEstimate(t, rec.Body.Bytes())
			if got.TotalTiles != want.TotalTiles || got.EstimatedSizeMB != want.EstimatedSizeMB {
				t.Errorf("total_tiles, estimated_size_mb = %d, %g; want the v2 estimate %d, %g", got.TotalTiles, got.EstimatedSizeMB, want.TotalTiles, want.EstimatedSizeMB)
			}
			if warning != tt.wantWarning {
				t.Errorf("warning = %q, want %q", warning, tt.wantWarning)
			}
		})
	}
}

// decodeEstimate returns the POST /api/estimate response in body and its warning field.
// The warning is "<absent>" when body has no warning field, so a test can tell an absent field from an empty one.
func decodeEstimate(t *testing.T, body []byte) (estimateResult, string) {
	t.Helper()
	var got struct {
		estimateResult
		Warning *string `json:"warning"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Warning == nil {
		return got.estimateResult, "<absent>"
	}
	return got.estimateResult, *got.Warning
}
