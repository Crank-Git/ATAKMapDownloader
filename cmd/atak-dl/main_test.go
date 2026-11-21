package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runMainEnv makes the test binary run main instead of the tests, so a test can read the exit code of main.
const runMainEnv = "ATAK_DL_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDownloadOfAGoogleSourceWithoutAcceptTermsExitsWithCode2(t *testing.T) {
	// The output directory is a regular file, so the engine fails before its first request if the terms check fails.
	// Thus Google gets no request from this test, even from a build without the check.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], //nolint:gosec // os.Args[0] is this test binary, and the arguments are constants.
		"--mapsources-dir", "../../mapsources",
		"download",
		"--source", "Google - Satellite Only",
		"--bbox", "40.7,-74.1,40.8,-74.0",
		"--min-zoom", "10", "--max-zoom", "10",
		"--output", filepath.Join(blocker, "out.sqlite"),
	)
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("Run() returned %v, want exit code 2; stderr %q", err, stderr.String())
	}
	const url = "https://developers.google.com/maps/documentation/tile/policies"
	if !strings.Contains(stderr.String(), url) {
		t.Errorf("stderr = %q, want it to hold %q", stderr.String(), url)
	}
}

func TestRootCommandPrintsTheBuildInfo(t *testing.T) {
	tests := []struct {
		name    string
		version string
		commit  string
		date    string
		want    string
	}{
		{
			name:    "prints the version, the commit and the build date that GoReleaser sets",
			version: "0.2.0-rc.1",
			commit:  "345bd1c",
			date:    "2026-11-21T10:00:00Z",
			want:    "atak-dl version 0.2.0-rc.1 (commit 345bd1c, built 2026-11-21T10:00:00Z)\n",
		},
		{
			name:    "prints the default values when the build sets no ldflags",
			version: "dev",
			commit:  "none",
			date:    "unknown",
			want:    "atak-dl version dev (commit none, built unknown)\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCmd(tt.version, tt.commit, tt.date)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs([]string{"--version"})

			if err := root.Execute(); err != nil {
				t.Fatalf("Execute() returned %v", err)
			}
			if got := out.String(); got != tt.want {
				t.Errorf("--version printed %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUserAgentNamesTheVersionAndTheProjectURL(t *testing.T) {
	want := "atak-map-downloader/0.2.0 (+https://github.com/Crank-Git/ATAKMapDownloader)"
	if got := userAgent("0.2.0"); got != want {
		t.Errorf("userAgent(%q) = %q, want %q", "0.2.0", got, want)
	}
}
