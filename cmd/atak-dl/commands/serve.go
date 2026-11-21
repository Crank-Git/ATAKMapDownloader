package commands

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// maxBodyBytes is the largest request body that the server reads (FR-security-hardening-17).
const maxBodyBytes = 1 << 20

// NewServeCmd returns the serve command, which starts the web interface.
func NewServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the web interface",
		RunE:  runServe,
	}
	cmd.Flags().String("addr", "localhost:5000", "Address to listen on")
	cmd.Flags().String("mapsources-dir", "mapsources", "Directory containing XML map source files")
	return cmd
}

func runServe(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("mapsources-dir")
	addr, _ := cmd.Flags().GetString("addr")

	loopback, err := isLoopbackAddr(addr)
	if err != nil {
		return err
	}

	parser := xmlparser.New(dir)
	sources := parser.DiscoverSources(false)
	if len(sources) == 0 {
		fmt.Fprintf(os.Stderr, "Warning: No map sources found in %s\n", dir)
	}

	// mbtiles.Create makes the directory again and reports that error.
	_ = os.MkdirAll(download.DownloadsDir(), 0755) //nolint:gosec // The mode 0755 is the existing behavior.

	calc := tilecalc.NewTileCalculator()
	handler := NewWebHandler(parser, calc, dir)

	if !loopback {
		// TODO(#58): Replace this warning with the token.
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: the server has no authentication and is reachable from other machines on %s\n", addr); err != nil {
			return fmt.Errorf("print the authentication warning: %w", err)
		}
	}
	fmt.Printf("ATAK Map Downloader - Web Interface\n")
	fmt.Printf("Open http://%s in your browser\n", addr)
	fmt.Printf("Mapsources: %s (%d sources)\n", dir, len(sources))

	srv := newServer(addr, handler)
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe() }()
	select {
	case err := <-served:
		return fmt.Errorf("serve on %s: %w", addr, err)
	case <-cmd.Context().Done():
		// Only a test cancels the context, because Execute gives serve a context that never ends.
		if err := srv.Close(); err != nil {
			return fmt.Errorf("close the server: %w", err)
		}
		return nil
	}
}

// isLoopbackAddr reports whether the host of addr is a loopback address or "localhost".
// An empty host listens on every interface, so it is not loopback.
// It returns an error when addr does not have the form host:port.
func isLoopbackAddr(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("read --addr %q: %w", addr, err)
	}
	if host == "localhost" {
		return true, nil
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback(), nil
}

// newServer returns the HTTP server for handler on addr, with the timeouts of FR-security-hardening-18.
// It has no WriteTimeout, because an SSE stream stays open for the full job.
// Every request body goes through limitBody.
func newServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           limitBody(handler),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

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
