// Package sourcesync replaces the synced ATAK-Maps source files in the user source directory with a GitHub release (FR-source-sync-10 to 14).
package sourcesync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Subdir is the subdirectory of the user source directory that a sync owns.
// A sync never touches a file outside it, so the user can keep source files beside it.
const Subdir = "atak-maps"

const (
	// maxArchiveBytes stops a wrong or hostile tarball URL from filling the memory. The v2.3.1 tarball is about 1 MiB.
	maxArchiveBytes = 64 << 20
	// maxExtractBytes stops a gzip stream that expands without limit. It is the sum of all entry bodies.
	maxExtractBytes = 256 << 20
	// apiVersion is the REST API version of the GitHub documentation that this package follows.
	apiVersion = "2026-03-10"
)

// Options holds the parameters of one sync.
type Options struct {
	// APIURL is the base URL of the GitHub REST API, for example https://api.github.com.
	APIURL string
	// Repo is the owner and the repository, for example joshuafuller/ATAK-Maps.
	Repo string
	// Tag is the release tag. An empty Tag selects the latest release.
	Tag string
	// Dir is the user source directory.
	Dir string
	// UserAgent goes on every request (FR-polite-downloading-14).
	UserAgent string
	Client    *http.Client
}

// Result is the release that a sync wrote and the number of source files that it added, changed and removed.
type Result struct {
	Tag     string
	Commit  string
	Added   int
	Changed int
	Removed int
}

// release holds the fields of a GitHub release that a sync reads.
type release struct {
	TagName    string `json:"tag_name"`
	TarballURL string `json:"tarball_url"`
}

// Sync replaces <Dir>/atak-maps/ with the .xml files under maps/ in the source tarball of a release.
// It also writes <Dir>/atak-maps/VERSION with the tag and the commit of the release.
// It returns an error and changes no file in these cases:
//   - a network error, or a response other than 200
//   - an archive path that is absolute or holds ..
//   - an archive with no .xml file under maps/
func Sync(ctx context.Context, opts Options) (Result, error) {
	rel, err := getRelease(ctx, opts)
	if err != nil {
		return Result{}, err
	}
	data, err := get(ctx, opts, rel.TarballURL, "")
	if err != nil {
		return Result{}, err
	}
	files, commit, err := extract(data)
	if err != nil {
		return Result{}, err
	}

	target := filepath.Join(opts.Dir, Subdir)
	before, err := hashTree(target)
	if err != nil {
		return Result{}, err
	}
	version := "tag: " + rel.TagName + "\ncommit: " + commit + "\n"
	if err := replace(opts.Dir, files, version); err != nil {
		return Result{}, err
	}

	res := Result{Tag: rel.TagName, Commit: commit}
	for name, body := range files {
		old, ok := before[name]
		switch {
		case !ok:
			res.Added++
		case old != sha256.Sum256(body):
			res.Changed++
		}
	}
	for name := range before {
		if _, ok := files[name]; !ok {
			res.Removed++
		}
	}
	return res, nil
}

// getRelease returns the release of opts.Tag, or the latest release when opts.Tag is empty.
// GitHub documents both calls at https://docs.github.com/en/rest/releases/releases.
func getRelease(ctx context.Context, opts Options) (release, error) {
	endpoint := opts.APIURL + "/repos/" + opts.Repo + "/releases/latest"
	if opts.Tag != "" {
		endpoint = opts.APIURL + "/repos/" + opts.Repo + "/releases/tags/" + url.PathEscape(opts.Tag)
	}
	data, err := get(ctx, opts, endpoint, "application/vnd.github+json")
	if err != nil {
		return release{}, err
	}
	var rel release
	if err := json.Unmarshal(data, &rel); err != nil {
		return release{}, fmt.Errorf("read the release from %s: %w", endpoint, err)
	}
	if rel.TagName == "" || rel.TarballURL == "" {
		return release{}, fmt.Errorf("the release from %s has no tag_name or no tarball_url", endpoint)
	}
	return rel, nil
}

// get returns the body of a GET request to endpoint, with at most maxArchiveBytes.
// An empty accept sends no Accept header.
func get(ctx context.Context, opts Options, endpoint, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("make the request for %s: %w", endpoint, err)
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := opts.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", endpoint, err)
	}
	// The body is read to the end or not needed, so a close error changes no result.
	defer func() { _ = resp.Body.Close() }()
	if err := checkStatus(resp, endpoint); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", endpoint, err)
	}
	if len(data) > maxArchiveBytes {
		return nil, fmt.Errorf("the response from %s is larger than %d MiB", endpoint, maxArchiveBytes>>20)
	}
	return data, nil
}

// checkStatus returns nil for a 200 response.
// For a rate-limit response, the error tells the user when to try again.
// GitHub documents the headers at https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api.
func checkStatus(resp *http.Response, endpoint string) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	retryAfter := resp.Header.Get("Retry-After")
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	limited := resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && (retryAfter != "" || remaining == "0"))
	if !limited {
		return fmt.Errorf("GitHub returned HTTP %d for %s", resp.StatusCode, endpoint)
	}
	msg := fmt.Sprintf("GitHub API rate limit reached (HTTP %d)", resp.StatusCode)
	if s, err := strconv.Atoi(retryAfter); err == nil {
		return fmt.Errorf("%s: try again after %d s", msg, s)
	}
	if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		return fmt.Errorf("%s: try again after %s", msg, time.Unix(reset, 0).UTC().Format(time.RFC3339))
	}
	return errors.New(msg + ": try again later")
}

// extract returns the .xml files under maps/ in the gzip tarball data, keyed by slash path, and the commit of the archive.
// GitHub puts every file under one top directory, so extract drops the first path element.
// git archive writes the commit to the comment record of the pax global header. Without one, the commit is "unknown".
func extract(data []byte) (map[string][]byte, string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("read the archive: %w", err)
	}
	limited := &io.LimitedReader{R: gz, N: maxExtractBytes}
	tr := tar.NewReader(limited)
	files := map[string][]byte{}
	commit := "unknown"
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("read the archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			if c := hdr.PAXRecords["comment"]; isHex(c) {
				commit = c
			}
			continue
		}
		name, err := mapsPath(hdr.Name)
		if err != nil {
			return nil, "", err
		}
		if name == "" || hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(strings.ToLower(name), ".xml") {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, "", fmt.Errorf("read %s from the archive: %w", hdr.Name, err)
		}
		files[name] = body
	}
	// The limit can end the stream on an entry boundary, and then the tar reader sees a clean end.
	if limited.N == 0 {
		return nil, "", fmt.Errorf("the archive is larger than %d MiB", maxExtractBytes>>20)
	}
	if len(files) == 0 {
		return nil, "", errors.New("the archive has no .xml file under maps/, so the upstream layout changed")
	}
	return files, commit, nil
}

// isHex reports whether s is a non-empty string of lowercase hexadecimal digits.
// A commit with another character could add lines to VERSION.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// mapsPath returns the path of an archive entry without its top directory, or "" when the entry is not under maps/.
// It returns an error for an absolute path or a path with a .. element, because such a path can leave the target directory (FR-source-sync-13).
func mapsPath(name string) (string, error) {
	if path.IsAbs(name) || filepath.IsAbs(name) || strings.Contains(name, `\`) {
		return "", fmt.Errorf("the archive path %q leaves the target directory", name)
	}
	for _, elem := range strings.Split(name, "/") {
		if elem == ".." {
			return "", fmt.Errorf("the archive path %q leaves the target directory", name)
		}
	}
	_, rest, ok := strings.Cut(name, "/")
	if !ok || !strings.HasPrefix(rest, "maps/") {
		return "", nil
	}
	if !filepath.IsLocal(filepath.FromSlash(rest)) {
		return "", fmt.Errorf("the archive path %q leaves the target directory", name)
	}
	// hashTree keys are clean paths, so a key such as maps/./a.xml must be clean too, or the counts are wrong.
	return path.Clean(rest), nil
}

// hashTree returns the SHA-256 of each .xml file under dir, keyed by slash path.
// A missing dir gives an empty map.
func hashTree(dir string) (map[string][32]byte, error) {
	hashes := map[string][32]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == dir {
			return fs.SkipAll
		}
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(p), ".xml") {
			return nil
		}
		data, err := os.ReadFile(p) //nolint:gosec // p is under the user source directory that the operator chooses.
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		hashes[filepath.ToSlash(rel)] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the synced source files in %s: %w", dir, err)
	}
	return hashes, nil
}

// replace writes files and VERSION into a temporary directory in dir, then swaps it into dir/atak-maps.
// The temporary directory is in dir, so each rename stays on one file system.
// If a step fails, dir/atak-maps keeps its old files.
func replace(dir string, files map[string][]byte, version string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("make the user source directory: %w", err)
	}
	if err := removeLeftovers(dir); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dir, ".atak-maps-new-")
	if err != nil {
		return fmt.Errorf("make a temporary directory: %w", err)
	}
	// A failed removal leaves only a hidden directory that the next sync removes.
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := writeAll(tmp, files, version); err != nil {
		return err
	}

	target := filepath.Join(dir, Subdir)
	aside, err := os.MkdirTemp(dir, ".atak-maps-old-")
	if err != nil {
		return fmt.Errorf("make a temporary directory: %w", err)
	}
	keepAside := false
	defer func() {
		if !keepAside {
			// A failed removal leaves only a hidden directory that the next sync removes.
			_ = os.RemoveAll(aside)
		}
	}()
	old := filepath.Join(aside, Subdir)
	if err := os.Rename(target, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("move the old synced source files aside: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		err = fmt.Errorf("move the new synced source files into place: %w", err)
		if restoreErr := os.Rename(old, target); restoreErr != nil && !errors.Is(restoreErr, fs.ErrNotExist) {
			// The old files are then only in aside, so aside must stay for the user.
			keepAside = true
			return errors.Join(err, fmt.Errorf("move the old synced source files back from %s: %w", old, restoreErr))
		}
		return err
	}
	return nil
}

// removeLeftovers removes the temporary directories of an earlier sync that the process did not finish.
// The parser reads every subdirectory, so a leftover directory would load its source files a second time.
func removeLeftovers(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read the user source directory: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() && (strings.HasPrefix(name, ".atak-maps-new-") || strings.HasPrefix(name, ".atak-maps-old-")) {
			if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
				return fmt.Errorf("remove the leftover directory %s: %w", name, err)
			}
		}
	}
	return nil
}

// writeAll writes files and VERSION under tmp through an os.Root, so no write can leave tmp.
func writeAll(tmp string, files map[string][]byte, version string) error {
	root, err := os.OpenRoot(tmp)
	if err != nil {
		return fmt.Errorf("open the temporary directory: %w", err)
	}
	// Each write reports its own error, so a close error changes no result.
	defer func() { _ = root.Close() }()
	for name, body := range files {
		if err := root.MkdirAll(path.Dir(name), 0o750); err != nil {
			return fmt.Errorf("make the directory of %s: %w", name, err)
		}
		if err := root.WriteFile(name, body, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if err := root.WriteFile("VERSION", []byte(version), 0o600); err != nil {
		return fmt.Errorf("write VERSION: %w", err)
	}
	return nil
}
