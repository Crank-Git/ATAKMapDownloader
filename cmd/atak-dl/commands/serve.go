package commands

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
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
	cmd.Flags().String("geocoder-url", defaultGeocoderURL, "Base URL of the Nominatim geocoder for place search")
	cmd.Flags().Bool("no-geocode", false, "Turn off place search and hide the search box in the web UI")
	cmd.Flags().Int("max-tiles", defaultMaxTiles, "Refuse a job API job above this tile count")
	cmd.Flags().Int("workers", 1, fmt.Sprintf("Number of jobs that run at the same time, 1 to %d", maxWorkers))
	cmd.Flags().String("token", "", "Token of a server that is not on loopback. The default is ATAKDL_TOKEN, else a generated token")
	return cmd
}

// maxWorkers is the highest value of serve --workers (feature 12, interfaces).
const maxWorkers = 8

func runServe(cmd *cobra.Command, userAgent string) error {
	addr, _ := cmd.Flags().GetString("addr")

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("read --addr %q: %w", addr, err)
	}
	// An empty host listens on every interface, so it is not loopback.
	ip := net.ParseIP(host)
	loopback := host == "localhost" || (ip != nil && ip.IsLoopback())

	parser := getParser(cmd)
	dir := parser.MapsourcesDir
	sources := parser.DiscoverSources()
	if len(sources) == 0 {
		fmt.Fprintf(os.Stderr, "Warning: No map sources found in %s\n", dir)
	}

	calc := tilecalc.NewTileCalculator()
	wh := NewWebHandler(parser, calc, dir, userAgent)
	// The store opens before the workers start, so a restored queued job runs (FR-team-server-10).
	if err := serveDataDir(cmd, wh); err != nil {
		return err
	}
	if err := applyGeocodeFlags(cmd, wh); err != nil {
		return err
	}
	if wh.maxTiles, err = cmd.Flags().GetInt("max-tiles"); err != nil {
		return fmt.Errorf("read --max-tiles: %w", err)
	}
	workers, err := serveWorkers(cmd)
	if err != nil {
		return err
	}
	if !loopback {
		if wh.auth, err = serverAuth(cmd); err != nil {
			return err
		}
	}
	handler := newServerHandler(wh)
	wh.queue.Start(cmd.Context(), workers)

	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "ATAK Map Downloader - Web Interface\nOpen http://%s in your browser\nMapsources: %s (%d sources)\n",
		addr, dir, len(sources)); err != nil {
		return fmt.Errorf("print the start message: %w", err)
	}

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

// serveWorkers returns the value of --workers (FR-team-server-2).
// It returns an error when the value is outside the range 1 to maxWorkers.
func serveWorkers(cmd *cobra.Command) (int, error) {
	workers, err := cmd.Flags().GetInt("workers")
	if err != nil {
		return 0, fmt.Errorf("read --workers: %w", err)
	}
	if workers < 1 || workers > maxWorkers {
		return 0, fmt.Errorf("--workers %d is outside the range 1-%d", workers, maxWorkers)
	}
	return workers, nil
}

// serveDataDir gives wh the data directory of cmd (FR-team-server-14).
// It returns the errors of dataDir and useDataDir.
func serveDataDir(cmd *cobra.Command, wh *WebHandler) error {
	dir, err := dataDir(cmd)
	if err != nil {
		return err
	}
	return wh.useDataDir(dir)
}

// serverAuth returns the token auth of a server that is not on loopback (FR-team-server-18, FR-team-server-19).
// The token comes from --token, else from ATAKDL_TOKEN.
// With neither, serverAuth generates a token and prints it once to stderr.
// It never prints a token from the flag or the environment, because the operator already has it.
func serverAuth(cmd *cobra.Command) (*tokenAuth, error) {
	token, err := cmd.Flags().GetString("token")
	if err != nil {
		return nil, fmt.Errorf("read --token: %w", err)
	}
	if token == "" {
		token = os.Getenv("ATAKDL_TOKEN")
	}
	if token == "" {
		if token, err = newToken(); err != nil {
			return nil, fmt.Errorf("generate the token: %w", err)
		}
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "token: %s (generated: set --token or ATAKDL_TOKEN to keep one token after a restart)\n", token); err != nil {
			return nil, fmt.Errorf("print the token: %w", err)
		}
	}
	return newTokenAuth(token), nil
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
