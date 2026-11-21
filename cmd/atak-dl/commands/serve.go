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

// NewServeCmd returns the serve command, which starts the web interface.
// Every tile request and every geocode request of the server sends userAgent.
func NewServeCmd(userAgent string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the web interface",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, userAgent)
		},
	}
	cmd.Flags().String("addr", "localhost:5000", "Address to listen on")
	cmd.Flags().String("mapsources-dir", "mapsources", "Directory containing XML map source files")
	return cmd
}

func runServe(cmd *cobra.Command, userAgent string) error {
	dir, _ := cmd.Flags().GetString("mapsources-dir")
	addr, _ := cmd.Flags().GetString("addr")

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("read --addr %q: %w", addr, err)
	}
	// An empty host listens on every interface, so it is not loopback.
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())

	parser := xmlparser.New(dir)
	sources := parser.DiscoverSources(false)
	if len(sources) == 0 {
		fmt.Fprintf(os.Stderr, "Warning: No map sources found in %s\n", dir)
	}

	// mbtiles.Create makes the directory again and reports that error.
	_ = os.MkdirAll(download.DownloadsDir(), 0755) //nolint:gosec // The mode 0755 is the existing behavior.

	calc := tilecalc.NewTileCalculator()
	handler := newServerHandler(NewWebHandler(parser, calc, dir, userAgent))

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

// newServer returns the HTTP server for handler on addr, with the timeouts of FR-security-hardening-18.
// It has no WriteTimeout, because an SSE stream stays open for the full job.
func newServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
