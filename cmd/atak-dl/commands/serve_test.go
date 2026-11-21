package commands

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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

// TestServerLimitsTheRequestBody checks the two body limits of FR-security-hardening-17.
// A body of 2 MiB passes the limit of an area route, and the decoder then rejects the open JSON with invalid_json.
func TestServerLimitsTheRequestBody(t *testing.T) {
	h := newServerHandler(newTestHandler(t, map[string]string{"test.xml": testSourceXML}))
	const twoMiB, sixMiB = 2 << 20, 6 << 20
	tests := []struct {
		name   string
		path   string
		body   io.Reader
		status int
		code   string
	}{
		{"reads a 2 MiB job body with a Content-Length", "/api/jobs", strings.NewReader(largeJSONBody(twoMiB)), http.StatusBadRequest, "invalid_json"},
		{"reads a 2 MiB estimate body without a Content-Length", "/api/estimate", unsizedReader{strings.NewReader(largeJSONBody(twoMiB))}, http.StatusBadRequest, "invalid_json"},
		{"reads a 2 MiB area tiles body", "/api/areas/tiles", strings.NewReader(largeJSONBody(twoMiB)), http.StatusBadRequest, "invalid_json"},
		{"rejects a 6 MiB job body with a Content-Length", "/api/jobs", strings.NewReader(largeJSONBody(sixMiB)), http.StatusRequestEntityTooLarge, "body_too_large"},
		{"rejects a 6 MiB job body without a Content-Length", "/api/jobs", unsizedReader{strings.NewReader(largeJSONBody(sixMiB))}, http.StatusRequestEntityTooLarge, "body_too_large"},
		{"rejects a 6 MiB estimate body without a Content-Length", "/api/estimate", unsizedReader{strings.NewReader(largeJSONBody(sixMiB))}, http.StatusRequestEntityTooLarge, "body_too_large"},
		{"rejects a 6 MiB area tiles body", "/api/areas/tiles", strings.NewReader(largeJSONBody(sixMiB)), http.StatusRequestEntityTooLarge, "body_too_large"},
		{"rejects a 2 MiB preview body, because a route with no areas keeps 1 MiB", "/api/preview", strings.NewReader(largeJSONBody(twoMiB)), http.StatusRequestEntityTooLarge, "body_too_large"},
		{"rejects a 2 MiB tile info body without a Content-Length", "/api/tile-info", unsizedReader{strings.NewReader(largeJSONBody(twoMiB))}, http.StatusRequestEntityTooLarge, "body_too_large"},
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

// runServeOnce runs serve with args and a cancelled context, so serve stops right after its start messages.
// It returns stdout and stderr.
func runServeOnce(t *testing.T, args ...string) (string, string) {
	t.Helper()
	stdout, stderr, err := executeServe(t, args...)
	if err != nil {
		t.Fatalf("Execute() returned %v", err)
	}
	return stdout, stderr
}

// executeServe runs serve like runServeOnce, and returns the error of Execute too.
func executeServe(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return executeRoot(t, append([]string{"serve"}, args...)...)
}

// executeRoot runs the root command with serve, with args after --mapsources-dir and a cancelled context.
func executeRoot(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	// serve makes the downloads directory in the working directory.
	t.Chdir(t.TempDir())
	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", "", "")
	root.PersistentFlags().String("data-dir", "", "")
	root.AddCommand(NewServeCmd(testUserAgent, "dev"))
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"--mapsources-dir", t.TempDir()}, args...))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := root.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

func TestServeAcceptsOneToEightWorkers(t *testing.T) {
	tests := []struct {
		workers string
		valid   bool
	}{
		{"0", false},
		{"1", true},
		{"8", true},
		{"9", false},
	}
	for _, tt := range tests {
		t.Run("--workers "+tt.workers, func(t *testing.T) {
			_, _, err := executeServe(t, "--workers", tt.workers)
			if tt.valid && err != nil {
				t.Errorf("Execute() returned %v, want no error", err)
			}
			if !tt.valid && (err == nil || !strings.Contains(err.Error(), "--workers")) {
				t.Errorf("Execute() returned %v, want an error that names --workers", err)
			}
		})
	}
}

// tokenLine matches the start line of a generated token (FR-team-server-19).
var tokenLine = regexp.MustCompile(`(?m)^token: ([0-9a-f]{32}) `)

func TestServePrintsAGeneratedTokenForANonLoopbackAddress(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		env   string
		print bool
	}{
		{"prints a token for 0.0.0.0", []string{"--addr", "0.0.0.0:0"}, "", true},
		{"prints a token for an empty host", []string{"--addr", ":0"}, "", true},
		{"prints no token for 127.0.0.1", []string{"--addr", "127.0.0.1:0"}, "", false},
		{"prints no token for localhost", []string{"--addr", "localhost:0"}, "", false},
		{"prints no token for ::1", []string{"--addr", "[::1]:0"}, "", false},
		{"prints no token from --token", []string{"--addr", "0.0.0.0:0", "--token", "flagtoken"}, "", false},
		{"prints no token from ATAKDL_TOKEN", []string{"--addr", "0.0.0.0:0"}, "envtoken", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ATAKDL_TOKEN", tt.env)
			logs := captureLog(t)
			stdout, stderr := runServeOnce(t, tt.args...)
			matches := tokenLine.FindAllStringSubmatch(stderr, -1)
			if tt.print != (len(matches) == 1) || len(matches) > 1 {
				t.Fatalf("stderr = %q, want one token line: %v", stderr, tt.print)
			}
			if strings.Contains(stderr, "no authentication") {
				t.Errorf("stderr = %q, want no authentication warning", stderr)
			}
			// A token from the flag or the environment is never printed, and a generated token goes to stderr only.
			everything := stdout + stderr + logs.String()
			if strings.Contains(everything, "flagtoken") || strings.Contains(everything, "envtoken") {
				t.Errorf("the output holds the token of the flag or the environment: %q", everything)
			}
			if tt.print && strings.Contains(stdout+logs.String(), matches[0][1]) {
				t.Errorf("stdout %q or the log %q holds the generated token", stdout, logs.String())
			}
		})
	}
}

func TestServeReadsTheDataDirectory(t *testing.T) {
	flagDir, envDir := t.TempDir(), t.TempDir()
	tests := []struct {
		name string
		args []string
		env  string
		want string
	}{
		{"uses ./downloads with no flag and no variable", nil, "", "downloads"},
		{"uses ATAKDL_DATA_DIR", nil, envDir, envDir},
		{"uses --data-dir", []string{"--data-dir", flagDir}, "", flagDir},
		{"uses --data-dir over ATAKDL_DATA_DIR", []string{"--data-dir", flagDir}, envDir, flagDir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ATAKDL_DATA_DIR", tt.env)
			// --data-dir is a flag of the root command, so it comes before serve.
			stdout, stderr, err := executeRoot(t, append(tt.args, "serve")...)
			if err != nil {
				t.Fatalf("Execute() returned %v; stdout %q, stderr %q", err, stdout, stderr)
			}
			if _, err := os.Stat(filepath.Join(tt.want, "jobs.jsonl")); err != nil {
				t.Errorf("jobs.jsonl in %s: %v", tt.want, err)
			}
		})
	}
}

func TestServeExitsWithCode1WhenTheDataDirectoryIsNotWritable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		dir  string
	}{
		{"refuses a directory below a regular file", filepath.Join(file, "data")},
		{"refuses a directory with no write permission", readOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.dir == readOnly && os.Geteuid() == 0 {
				t.Skip("root writes to a directory with no write permission")
			}
			_, _, err := executeRoot(t, "--data-dir", tt.dir, "serve")
			if got := ExitCode(err); got != 1 {
				t.Errorf("exit code = %d, want 1", got)
			}
			if want := "data directory is not writable: " + tt.dir; err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("Execute() returned %v, want the message %q", err, want)
			}
		})
	}
}
