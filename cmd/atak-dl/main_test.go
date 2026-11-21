package main

import (
	"bytes"
	"context"
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

// The test names the source by its v0.1 name, so it also checks the alias to the v2 name (FR-source-sync-16).
func TestDownloadOfAGoogleSourceWithoutAcceptTermsExitsWithCode2(t *testing.T) {
	// The output directory is a regular file, so the engine fails before its first request if the terms check fails.
	// Thus Google gets no request from this test, even from a build without the check.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], //nolint:gosec // os.Args[0] is this test binary, and the arguments are constants.
		"--mapsources-dir", t.TempDir(),
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
	for _, want := range []string{
		"https://developers.google.com/maps/documentation/tile/policies",
		`Warning: "Google - Satellite Only" is an old map source name. The new name is "Google Satellite".`,
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to hold %q", stderr.String(), want)
		}
	}
}

func TestDownloadOfARemovedSourceNamesTheUpstreamVersion(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], //nolint:gosec // os.Args[0] is this test binary, and the arguments are constants.
		"--mapsources-dir", t.TempDir(),
		"download",
		"--source", "Michelin - OSM Michelin",
		"--bbox", "40.7,-74.1,40.8,-74.0",
		"--min-zoom", "10", "--max-zoom", "10",
		"--output", filepath.Join(t.TempDir(), "out.sqlite"),
	)
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("Run() returned %v, want exit code 1; stderr %q", err, stderr.String())
	}
	const want = "map source removed upstream in ATAK-Maps v1.10.1"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to hold %q", stderr.String(), want)
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

// setCleanMachine gives the test a new home directory and working directory, so no user source directory and no mapsources/ folder exist.
func setCleanMachine(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	t.Chdir(t.TempDir())
}

func TestMapsourcesDirDefaultsToTheUserConfigDirectory(t *testing.T) {
	setCleanMachine(t)
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(config, "atak-dl", "mapsources")
	got := newRootCmd("dev", "none", "unknown").PersistentFlags().Lookup("mapsources-dir").DefValue
	if got != want {
		t.Errorf("--mapsources-dir default = %q, want %q", got, want)
	}
}

func TestListSourcesOnACleanMachineListsTheSnapshotSources(t *testing.T) {
	setCleanMachine(t)
	root := newRootCmd("dev", "none", "unknown")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"list-sources"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() returned %v", err)
	}
	for _, want := range []string{
		"  Google Satellite (jpg) - Zoom 0-20 - None - restricted - tile - snapshot\n",
		"  USGS Topo (jpg) - Zoom 0-16 - None - open - tile - snapshot\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list-sources printed %q, want it to hold %q", out.String(), want)
		}
	}
}

func TestServeLoadsTheSourceFilesOfTheGlobalMapsourcesDir(t *testing.T) {
	setCleanMachine(t)
	dir := t.TempDir()
	xml := `<customMapSource><name>Serve Test</name><url>https://example.com/{$z}/{$x}/{$y}.png</url></customMapSource>`
	if err := os.WriteFile(filepath.Join(dir, "serve.xml"), []byte(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newRootCmd("dev", "none", "unknown")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--mapsources-dir", dir, "--no-snapshot", "serve", "--addr", "127.0.0.1:0"})

	// A cancelled context makes serve stop at once, after it loads the source catalog.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("Execute() returned %v", err)
	}
	want := "Mapsources: " + dir + " (1 sources)\n"
	if !strings.Contains(out.String(), want) {
		t.Errorf("serve printed %q, want it to hold %q", out.String(), want)
	}
}
