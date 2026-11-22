package commands

import (
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
)

func TestEstimateRejectsAZoomRangeThatIsNotMinMax(t *testing.T) {
	tests := []struct {
		name      string
		zoomRange string
	}{
		{"rejects a value with no dash", "abc"},
		{"rejects a maximum that is not a number", "5-x"},
		{"rejects a minimum that is not a number", "x-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			_, _, err := runEstimateArgs(t, srv, "--bbox", oneTileBBox(), "--zoom-range", tt.zoomRange)
			if err == nil || !strings.Contains(err.Error(), "is not min-max") || ExitCode(err) != 1 {
				t.Errorf("estimate --zoom-range %q error = %v (exit code %d), want exit code 1 and \"is not min-max\"", tt.zoomRange, err, ExitCode(err))
			}
			if n := srv.TotalRequests(); n != 0 {
				t.Errorf("tile server got %d requests, want 0", n)
			}
		})
	}
}
