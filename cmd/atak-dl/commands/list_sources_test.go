package commands

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/snapshot"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// runListSourcesCmd returns the output of list-sources with the user source directory dir and the flags in args.
func runListSourcesCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", dir, "")
	root.PersistentFlags().Bool("no-snapshot", false, "")
	root.AddCommand(NewListSourcesCmd())
	var out bytes.Buffer
	root.SetOut(&out)
	// Cobra reads a global flag after the command name too, so args can hold global and local flags.
	root.SetArgs(append([]string{"list-sources"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() returned %v", err)
	}
	return out.String()
}

func TestListSourcesShowsRestrictedForGoogleBingAndEsri(t *testing.T) {
	// The test reads the snapshot source files, so a new Google, Bing or Esri file is checked too.
	lines := strings.Split(runListSourcesCmd(t, filepath.Join(t.TempDir(), "missing")), "\n")

	for _, pattern := range []string{
		"maps/base/google/*.xml",
		"maps/base/bing/*.xml",
		"maps/base/esri/*.xml",
		"maps/overlays/grg_google_*.xml",
	} {
		files, err := fs.Glob(snapshot.FS(), pattern)
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, file := range files {
			src, err := xmlparser.ParseFS(snapshot.FS(), file)
			if err != nil || src == nil {
				continue
			}
			checked++
			line := findLine(lines, "  "+src.Name+" (")
			if !strings.Contains(line, " - restricted - ") {
				t.Errorf("%s: list-sources line = %q, want the class restricted", file, line)
			}
		}
		if checked == 0 {
			t.Errorf("found no source files for %s in the snapshot", pattern)
		}
	}
}

func TestListSourcesShowsTheClassKindAndOriginOfEachSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.xml"), []byte(testSourceXML), 0o600); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(runListSourcesCmd(t, dir), "\n")
	tests := []struct {
		prefix string
		want   string
	}{
		{"  Test Source (", "  Test Source (png) - Zoom 0-18 - None - conditional - tile - user"},
		{"  Google Satellite (", "  Google Satellite (jpg) - Zoom 0-20 - None - restricted - tile - snapshot"},
	}
	for _, tt := range tests {
		if line := findLine(lines, tt.prefix); line != tt.want {
			t.Errorf("list-sources line = %q, want %q", line, tt.want)
		}
	}
}

func TestListSourcesShowsAUserSourceInPlaceOfTheSnapshotSourceOfTheSameName(t *testing.T) {
	dir := t.TempDir()
	xml := strings.Replace(testSourceXML, "Test Source", "Google Satellite", 1)
	if err := os.WriteFile(filepath.Join(dir, "mine.xml"), []byte(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	const want = "  Google Satellite (png) - Zoom 0-18 - None - conditional - tile - user"
	if line := findLine(strings.Split(runListSourcesCmd(t, dir), "\n"), "  Google Satellite ("); line != want {
		t.Errorf("list-sources line = %q, want %q", line, want)
	}
}

func TestListSourcesWithNoSnapshotListsOnlyTheUserSourceDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.xml"), []byte(testSourceXML), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runListSourcesCmd(t, dir, "--no-snapshot")
	if !strings.Contains(out, "\nFound 1 map sources\n") || strings.Contains(out, "Google Satellite") {
		t.Errorf("list-sources --no-snapshot printed %q, want only Test Source", out)
	}
}

func TestListSourcesSkippedListsEverySnapshotSourceFileThatDidNotLoad(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	files := 0
	err := fs.WalkDir(snapshot.FS(), "maps", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".xml") {
			files++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// The acceptance criterion of issue #38 names 53 source files in the pinned snapshot.
	if files != 53 {
		t.Fatalf("the snapshot holds %d source files, want 53", files)
	}

	var loaded int
	for _, line := range strings.Split(runListSourcesCmd(t, missing), "\n") {
		if _, err := fmt.Sscanf(line, "Found %d map sources", &loaded); err == nil {
			break
		}
	}

	reasons := map[string]bool{
		xmlparser.SkipParseError:         true,
		xmlparser.SkipMissingName:        true,
		xmlparser.SkipMissingURL:         true,
		xmlparser.SkipUnsupportedCRS:     true,
		xmlparser.SkipUnknownPlaceholder: true,
		xmlparser.SkipDuplicateName:      true,
	}
	out := strings.TrimRight(runListSourcesCmd(t, missing, "--skipped"), "\n")
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		fields := strings.SplitN(line, "  ", 3)
		if len(fields) != 3 || !strings.HasPrefix(fields[0], "maps/") || !reasons[fields[1]] || fields[2] == "" {
			t.Errorf("list-sources --skipped line = %q, want path  reason  detail", line)
		}
	}
	if loaded+len(lines) != files {
		t.Errorf("loaded %d + skipped %d = %d, want %d source files", loaded, len(lines), loaded+len(lines), files)
	}
}

func TestListSourcesSkippedReportsNoSkippedSourceFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.xml"), []byte(testSourceXML), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := runListSourcesCmd(t, dir, "--no-snapshot", "--skipped"); out != "No skipped source files.\n" {
		t.Errorf("list-sources --skipped printed %q, want the empty message", out)
	}
}

func findLine(lines []string, prefix string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}
