package commands

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestNewServerSetsTheTimeoutsOfFRSecurityHardening18(t *testing.T) {
	srv := newServer("localhost:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Errorf("ReadTimeout = %v, want 30s", srv.ReadTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("IdleTimeout = %v, want 120s", srv.IdleTimeout)
	}
	// An SSE stream stays open for the full job, so a write timeout would end it.
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0", srv.WriteTimeout)
	}
}

// largeJSONBody returns a JSON prefix of n bytes that a decoder must read to its end before it can fail.
func largeJSONBody(n int) string {
	return `{"name":"` + strings.Repeat("a", n)
}

// unsizedReader hides the length of r, so that the request has no Content-Length, as a chunked body has.
type unsizedReader struct{ r io.Reader }

func (u unsizedReader) Read(p []byte) (int, error) { return u.r.Read(p) }

func TestServerRejectsARequestBodyLargerThan1MiB(t *testing.T) {
	h := newServerHandler(newTestHandler(t, map[string]string{"test.xml": testSourceXML}))
	const twoMiB = 2 << 20
	tests := []struct {
		name   string
		path   string
		body   io.Reader
		status int
		code   string
	}{
		{"rejects a 2 MiB job body with a Content-Length", "/api/jobs", strings.NewReader(largeJSONBody(twoMiB)), http.StatusRequestEntityTooLarge, "body_too_large"},
		{"rejects a 2 MiB job body without a Content-Length", "/api/jobs", unsizedReader{strings.NewReader(largeJSONBody(twoMiB))}, http.StatusRequestEntityTooLarge, "body_too_large"},
		{"rejects a 2 MiB estimate body without a Content-Length", "/api/estimate", unsizedReader{strings.NewReader(largeJSONBody(twoMiB))}, http.StatusRequestEntityTooLarge, "body_too_large"},
		{"reads a body below 1 MiB", "/api/jobs", strings.NewReader(largeJSONBody(1000)), http.StatusBadRequest, "invalid_json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, tt.body)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			checkError(t, rec.Result(), tt.status, tt.code)
		})
	}
}

func TestServePrintsTheNoAuthenticationWarningForANonLoopbackAddress(t *testing.T) {
	tests := []struct {
		addr string
		warn bool
	}{
		{"0.0.0.0:0", true},
		{":0", true},
		{"127.0.0.1:0", false},
		{"localhost:0", false},
		{"[::1]:0", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			// serve makes the downloads directory in the working directory.
			t.Chdir(t.TempDir())
			root := &cobra.Command{Use: "atak-dl"}
			root.AddCommand(NewServeCmd(testUserAgent))
			var stderr bytes.Buffer
			root.SetErr(&stderr)
			root.SetArgs([]string{"serve", "--addr", tt.addr, "--mapsources-dir", t.TempDir()})

			// A cancelled context makes serve stop at once, after it prints the warning.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := root.ExecuteContext(ctx); err != nil {
				t.Fatalf("Execute() returned %v", err)
			}
			warning := "warning: the server has no authentication and is reachable from other machines on " + tt.addr + "\n"
			if got := strings.Contains(stderr.String(), warning); got != tt.warn {
				t.Errorf("stderr = %q, want the warning: %v", stderr.String(), tt.warn)
			}
		})
	}
}
