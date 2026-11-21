package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// syncTarball returns a gzip tarball with the files in entries, each as a name and a body.
func syncTarball(t *testing.T, entries ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag:   tar.TypeXGlobalHeader,
		Name:       "pax_global_header",
		PAXRecords: map[string]string{"comment": "bc5d9d541ca1f2086ecf21c78e3dd9f459e13f40"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: e[0], Mode: 0o644, Size: int64(len(e[1]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// runSourcesSync points sources sync at a fake GitHub API that serves the latest release v2.4.0 with the tarball body.
// It returns the output and the error of the command for the user source directory dir.
func runSourcesSync(t *testing.T, dir string, body []byte) (string, error) {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("GET /repos/joshuafuller/ATAK-Maps/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v2.4.0","tarball_url":"` + srv.URL + `/tarball"}`))
	})
	mux.HandleFunc("GET /tarball", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := githubAPIURL
	githubAPIURL = srv.URL
	t.Cleanup(func() { githubAPIURL = old })

	root := &cobra.Command{Use: "atak-dl", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("mapsources-dir", dir, "")
	root.AddCommand(NewSourcesCmd("atak-map-downloader/test"))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"sources", "sync"})
	err := root.Execute()
	return out.String(), err
}

func TestSourcesSyncWritesTheXMLFilesAndVERSIONAndPrintsTheCounts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mapsources")
	out, err := runSourcesSync(t, dir, syncTarball(t,
		[2]string{"top/maps/base/osm/osm.xml", "<osm/>"},
		[2]string{"top/maps/overlays/roads.xml", "<roads/>"},
	))
	if err != nil {
		t.Fatalf("Execute() returned %v", err)
	}
	if want := "v2.4.0: 2 added, 0 changed, 0 removed\n"; out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
	for _, rel := range []string{"atak-maps/maps/base/osm/osm.xml", "atak-maps/maps/overlays/roads.xml", "atak-maps/VERSION"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("Stat(%s) returned %v", rel, err)
		}
	}
}

func TestSourcesSyncWithAnArchivePathWithDotDotExitsWithCode1AndChangesNothing(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "my-source.xml")
	if err := os.WriteFile(user, []byte("<mine/>"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runSourcesSync(t, dir, syncTarball(t,
		[2]string{"top/maps/good.xml", "<good/>"},
		[2]string{"../evil.xml", "<evil/>"},
	))
	if code := ExitCode(err); code != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", err, code)
	}
	if !strings.Contains(err.Error(), "existing source files kept") {
		t.Errorf("error = %q, want it to say that the existing source files stay", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "my-source.xml" {
		t.Errorf("the user source directory holds %v, want only my-source.xml", entries)
	}
}
