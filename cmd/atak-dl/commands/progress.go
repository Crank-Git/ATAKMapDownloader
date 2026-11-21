package commands

import (
	"fmt"
	"strconv"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
)

// progressLine returns the CLI progress line for p, in the form of feature 07 § Screens & states.
func progressLine(p download.Progress) string {
	eta := time.Duration(p.ETASeconds() * float64(time.Second))
	return fmt.Sprintf("%.1f%% (%s/%s) - %.1f tiles/s - %s skipped - %s failed - ETA %s",
		p.ProgressPercent(), formatCount(p.Processed()), formatCount(int64(p.TotalTiles)), p.TilesPerSecond(),
		formatCount(p.Skipped), formatCount(p.Failed), formatDuration(eta))
}

// summaryLine returns the final summary line of a job that took elapsed and wrote output (FR-robust-downloads-29).
func summaryLine(p download.Progress, elapsed time.Duration, output string) string {
	return fmt.Sprintf("Done: %s downloaded, %s skipped, %s failed in %s -> %s",
		formatCount(p.Downloaded), formatCount(p.Skipped), formatCount(p.Failed), formatDuration(elapsed), output)
}

// formatCount returns n with a comma between each group of three digits, for example 120,000.
func formatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// formatDuration returns d rounded to seconds, in the form 1h43m, 1m5s or 5s.
// A duration of one hour or more drops the seconds, because the progress line only needs the minutes.
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
