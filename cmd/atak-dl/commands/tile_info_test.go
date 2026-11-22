package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runTileInfoArgs runs tile-info with args after "tile-info", and returns stdout and the error of Execute.
func runTileInfoArgs(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", t.TempDir(), "")
	root.PersistentFlags().Bool("no-snapshot", true, "")
	root.AddCommand(NewTileInfoCmd())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"tile-info"}, args...))
	err := root.Execute()
	return out.String(), err
}

func TestTileInfoRejectsAFlagValueThatIsNotANumber(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"rejects a latitude that is not a number", []string{"--lat", "north", "--lon", "-121.7", "--zoom", "12"}, `invalid argument "north" for "--lat" flag`},
		{"rejects a longitude that is not a number", []string{"--lat", "45.37", "--lon", "west", "--zoom", "12"}, `invalid argument "west" for "--lon" flag`},
		{"rejects a zoom level that is not a whole number", []string{"--lat", "45.37", "--lon", "-121.7", "--zoom", "1.5"}, `invalid argument "1.5" for "--zoom" flag`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runTileInfoArgs(t, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) || ExitCode(err) != 1 {
				t.Errorf("tile-info error = %v (exit code %d), want exit code 1 and an error that holds %q", err, ExitCode(err), tt.want)
			}
			if strings.Contains(out, "Tile Information") {
				t.Errorf("stdout = %q, want no tile information", out)
			}
		})
	}
}

func TestTileInfoReturnsTheReadErrorOfAMissingFlag(t *testing.T) {
	tests := []struct {
		name  string
		flags []string
		want  string
	}{
		{"returns the --lat error", []string{"lon", "zoom"}, "read --lat"},
		{"returns the --lon error", []string{"lat", "zoom"}, "read --lon"},
		{"returns the --zoom error", []string{"lat", "lon"}, "read --zoom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A command without the flag is the one way to make the flag read fail after cobra parsed the arguments.
			cmd := &cobra.Command{Use: "tile-info"}
			for _, f := range tt.flags {
				if f == "zoom" {
					cmd.Flags().Int(f, 10, "")
				} else {
					cmd.Flags().Float64(f, 0, "")
				}
			}
			root := &cobra.Command{Use: "atak-dl"}
			root.PersistentFlags().String("mapsources-dir", t.TempDir(), "")
			root.PersistentFlags().Bool("no-snapshot", true, "")
			root.AddCommand(cmd)
			err := runTileInfo(cmd, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) || ExitCode(err) != 1 {
				t.Errorf("runTileInfo() = %v (exit code %d), want exit code 1 and an error that holds %q", err, ExitCode(err), tt.want)
			}
		})
	}
}
