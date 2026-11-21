package commands

import "net/http"

// healthResponse is the body of GET /healthz (FR-team-server-29).
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// serveHealth writes status ok and the version of the binary, so that a monitor can check the server.
// The token check passes /healthz, because a monitor has no token (FR-team-server-17).
// A method other than GET gets 405 method_not_allowed.
func (h *WebHandler) serveHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		return
	}
	writeJSON(w, healthResponse{Status: "ok", Version: h.version})
}
