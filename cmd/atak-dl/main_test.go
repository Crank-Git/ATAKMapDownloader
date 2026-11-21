package main

import (
	"bytes"
	"testing"
)

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
