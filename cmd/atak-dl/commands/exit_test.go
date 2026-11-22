package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/spf13/cobra"
)

func TestCommandErrorsPrintTheSpecMessageAndExitWithCode1(t *testing.T) {
	tests := []struct {
		name     string
		tileType string
		args     []string
		want     string
	}{
		{"validate-source names a missing map source", "png",
			[]string{"validate-source", "--source", "missing"}, "Source 'missing' not found\n"},
		{"validate-source lists the validation errors", "gif",
			[]string{"validate-source", "--source", "Test TLS"}, "✗ Source 'Test TLS' has validation errors:\n  - unsupported tile type: gif\n"},
		{"estimate names a missing map source", "png",
			[]string{"estimate", "--source", "missing", "--bbox", oneTileBBox()}, "Source 'missing' not found\n"},
		{"estimate rejects a zoom level outside the source zoom range", "png",
			[]string{"estimate", "--source", "Test TLS", "--bbox", oneTileBBox(), "--zoom", "19"}, "No valid zoom levels for this source\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			root := &cobra.Command{Use: "atak-dl"}
			root.PersistentFlags().String("mapsources-dir", "", "")
			root.AddCommand(NewValidateSourceCmd(), NewEstimateCmd())
			var stderr bytes.Buffer
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&stderr)
			root.SetArgs(append([]string{"--mapsources-dir", writeTypedSource(t, srv, "Test TLS", tt.tileType)}, tt.args...))
			err := root.Execute()
			if err == nil || ExitCode(err) != 1 {
				t.Fatalf("Execute() = %v (exit code %d), want exit code 1", err, ExitCode(err))
			}
			// The spec names the stderr text verbatim, so cobra adds no "Error: " prefix and no usage text.
			if got := stderr.String(); got != tt.want {
				t.Errorf("stderr = %q, want exactly %q", got, tt.want)
			}
		})
	}
}

func TestEstimateRejectsABoundingBoxThatIsNotValid(t *testing.T) {
	tests := []struct {
		name string
		bbox string
		want string
	}{
		{"rejects three values", "1,2,3", "must have 4 values"},
		{"rejects a value that is not a number", "1,2,x,4", "invalid bbox value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			_, _, err := runEstimateArgs(t, srv, "--bbox", tt.bbox, "--zoom", "10")
			if err == nil || !strings.Contains(err.Error(), tt.want) || ExitCode(err) != 1 {
				t.Errorf("estimate --bbox %q error = %v (exit code %d), want exit code 1 and %q", tt.bbox, err, ExitCode(err), tt.want)
			}
		})
	}
}
