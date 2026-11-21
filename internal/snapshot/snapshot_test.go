package snapshot

import (
	"io/fs"
	"strings"
	"testing"
)

func TestVersionNamesTheATAKMapsTagAndCommit(t *testing.T) {
	data, err := fs.ReadFile(FS(), "VERSION")
	if err != nil {
		t.Fatal(err)
	}
	const want = "tag: v2.3.1\ncommit: bc5d9d541ca1f2086ecf21c78e3dd9f459e13f40\n"
	if string(data) != want {
		t.Errorf("VERSION = %q, want %q", data, want)
	}
}

func TestSnapshotHoldsTheATAKMapsSourceFilesInTheUpstreamLayout(t *testing.T) {
	var files []string
	err := fs.WalkDir(FS(), "maps", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".xml") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// ATAK-Maps v2.3.1 has 53 source files under maps/.
	if len(files) != 53 {
		t.Errorf("found %d source files under maps/, want 53", len(files))
	}
	for _, path := range []string{"maps/base/google/google_satellite.xml", "maps/overlays/grg_fema_flood.xml"} {
		if _, err := fs.Stat(FS(), path); err != nil {
			t.Errorf("Stat(%q) returned %v", path, err)
		}
	}
}

func TestSnapshotCarriesTheATAKMapsLicenseAndTheNotice(t *testing.T) {
	license, err := fs.ReadFile(FS(), "LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(license), "MIT License") || !strings.Contains(string(license), "Joshua Fuller") {
		t.Errorf("LICENSE does not hold the ATAK-Maps MIT license: %q", license)
	}
	notice, err := fs.ReadFile(FS(), "NOTICE.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ATAK-Maps", "https://github.com/joshuafuller/ATAK-Maps", "MIT", "v2.3.1"} {
		if !strings.Contains(string(notice), want) {
			t.Errorf("NOTICE.md does not hold %q", want)
		}
	}
}
