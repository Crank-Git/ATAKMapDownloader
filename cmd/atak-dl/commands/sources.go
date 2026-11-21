package commands

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/sourcesync"
	"github.com/spf13/cobra"
)

// githubAPIURL is the base URL of the GitHub REST API. Tests replace it, because a test never calls the real GitHub.
var githubAPIURL = "https://api.github.com"

// syncTimeout bounds one sync, so a stalled GitHub connection cannot hold the command.
const syncTimeout = 5 * time.Minute

// NewSourcesCmd returns the sources command group.
// Its sync subcommand sends userAgent on every request to GitHub.
func NewSourcesCmd(userAgent string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sources",
		Short: "Manage the source files in the user source directory",
	}
	cmd.AddCommand(newSourcesSyncCmd(userAgent))
	return cmd
}

// newSourcesSyncCmd returns sources sync, which writes an ATAK-Maps release to <user source directory>/atak-maps/.
// It prints "<tag>: N added, N changed, N removed" (FR-source-sync-14).
// On any failure it keeps the existing source files and exits with code 1.
func newSourcesSyncCmd(userAgent string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Download the latest ATAK-Maps release into <user source directory>/atak-maps",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return syncSources(cmd, userAgent)
		},
	}
	cmd.Flags().String("tag", "", "Release tag to download, for example v2.3.1. Without it, the command uses the latest release")
	return cmd
}

func syncSources(cmd *cobra.Command, userAgent string) error {
	tag, err := cmd.Flags().GetString("tag")
	if err != nil {
		return fmt.Errorf("read --tag: %w", err)
	}
	dir, err := cmd.Root().PersistentFlags().GetString("mapsources-dir")
	if err != nil {
		return fmt.Errorf("read --mapsources-dir: %w", err)
	}
	if dir == "" {
		return &ExitError{Code: 1, Err: errors.New("sync failed: no user source directory, so set --mapsources-dir")}
	}

	res, err := sourcesync.Sync(cmd.Context(), sourcesync.Options{
		APIURL:    githubAPIURL,
		Repo:      "joshuafuller/ATAK-Maps",
		Tag:       tag,
		Dir:       dir,
		UserAgent: userAgent,
		Client:    &http.Client{Timeout: syncTimeout},
	})
	if err != nil {
		return &ExitError{Code: 1, Err: fmt.Errorf("sync failed, existing source files kept: %w", err)}
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %d added, %d changed, %d removed\n",
		res.Tag, res.Added, res.Changed, res.Removed); err != nil {
		return fmt.Errorf("write the sync counts: %w", err)
	}
	return nil
}
