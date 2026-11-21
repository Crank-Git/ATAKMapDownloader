package commands

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestHealthzReturnsStatusAndVersion checks the health route of FR-team-server-29 and its token exemption of FR-team-server-17.
func TestHealthzReturnsStatusAndVersion(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		loopback bool
		status   int
	}{
		{"returns 200 for a GET on loopback", http.MethodGet, true, http.StatusOK},
		{"returns 200 for a GET with no token on a server that is not on loopback", http.MethodGet, false, http.StatusOK},
		{"rejects a POST", http.MethodPost, true, http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(t, nil)
			h.version = "1.2.3"
			if !tt.loopback {
				h.auth = newTokenAuth(testToken)
			}
			resp := serve(newServerHandler(h), tt.method, "/healthz", "").Result()
			if tt.status != http.StatusOK {
				checkError(t, resp, tt.status, "method_not_allowed")
				return
			}
			defer closeBody(resp)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("body does not parse: %v", err)
			}
			if len(body) != 2 || body["status"] != "ok" || body["version"] != "1.2.3" {
				t.Errorf("body = %v, want status ok and version 1.2.3", body)
			}
		})
	}
}
