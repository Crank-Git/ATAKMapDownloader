package commands

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestFileRoutesCheckTheHandleThatTheyOpen(t *testing.T) {
	// A check by name and a later open by name leave a window to swap the file, so each route checks an open handle.
	b, err := os.ReadFile("files.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	open := regexp.MustCompile(`(?s)\nfunc openOutputFile\(.*?\n}\n`).FindString(src)
	if open == "" {
		t.Fatal("files.go has no openOutputFile")
	}
	if o, s := strings.Index(open, "root.Open(name)"), strings.Index(open, "f.Stat()"); o < 0 || s < o {
		t.Error("openOutputFile does not check the Stat of the handle that root.Open returns")
	}
	if outside := strings.Count(src, ".Lstat(") - strings.Count(open, ".Lstat("); outside != 0 {
		t.Errorf("files.go calls Lstat %d times outside openOutputFile, want 0", outside)
	}
	for _, fn := range []string{"serveTile", "outputFile", "downloadFile"} {
		body := regexp.MustCompile(`(?s)\nfunc \(h \*WebHandler\) ` + fn + `\(.*?\n}\n`).FindString(src)
		if !strings.Contains(body, "openOutputFile(root, name)") {
			t.Errorf("%s does not open the file through openOutputFile", fn)
		}
	}
}

func TestFileRoutesRejectASymbolicLinkInsideTheOutputDirectory(t *testing.T) {
	h := filesHandler(t)
	writeOutputFile(t, filepath.Join(h.outputDir, "real.mbtiles"), fileMetadata)
	// os.Root follows a relative link that stays inside the root, so only a check of the name itself rejects it.
	if err := os.Symlink("real.mbtiles", filepath.Join(h.outputDir, "link.mbtiles")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/files/link.mbtiles", "/api/files/link.mbtiles/tiles/10/301/385"} {
		t.Run(path, func(t *testing.T) {
			checkError(t, serve(h, http.MethodGet, path, "").Result(), http.StatusNotFound, "file_not_found")
		})
	}
}
