package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// The test reads the bundled source files, so a new Google, Bing or Esri file is checked too.
const bundledSourcesDir = "../../../mapsources"

func TestListSourcesShowsRestrictedForGoogleBingAndEsri(t *testing.T) {
	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", bundledSourcesDir, "")
	root.AddCommand(NewListSourcesCmd())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"list-sources"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() returned %v", err)
	}
	lines := strings.Split(out.String(), "\n")

	parser := xmlparser.New(bundledSourcesDir)
	for _, provider := range []string{"Google", "Bing", "ESRI"} {
		files, err := filepath.Glob(filepath.Join(bundledSourcesDir, provider, "*.xml"))
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, file := range files {
			src, err := parser.ParseFile(file)
			if err != nil || src == nil {
				continue
			}
			checked++
			line := findLine(lines, "  "+src.Name+" (")
			if !strings.HasSuffix(line, " - restricted") {
				t.Errorf("%s: list-sources line = %q, want a line that ends with \" - restricted\"", file, line)
			}
		}
		if checked == 0 {
			t.Errorf("found no %s source files in %s", provider, bundledSourcesDir)
		}
	}
}

func TestListSourcesShowsTheClassOfEachSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.xml"), []byte(testSourceXML), 0o600); err != nil {
		t.Fatal(err)
	}
	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", dir, "")
	root.AddCommand(NewListSourcesCmd())
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"list-sources"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() returned %v", err)
	}
	want := "  Test Source (png) - Zoom 0-18 - None - conditional"
	if line := findLine(strings.Split(out.String(), "\n"), "  Test Source ("); line != want {
		t.Errorf("list-sources line = %q, want %q", line, want)
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
