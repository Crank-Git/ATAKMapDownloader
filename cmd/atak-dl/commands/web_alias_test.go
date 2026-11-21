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
