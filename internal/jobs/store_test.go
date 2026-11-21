package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// storeLine returns one jobs.jsonl line for the job id in state.
func storeLine(id, state string) string {
	return fmt.Sprintf(`{"id":%q,"state":%q}`, id, state)
}

// writeStoreFile writes lines as the jobs.jsonl file in a new directory and returns its path.
func writeStoreFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fileLines returns the lines of the file at path.
func fileLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // The test reads the file in its own temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// lineStrings returns the lines of s as strings.
func lineStrings(s *Store) []string {
	var out []string
	for _, l := range s.Lines() {
		out = append(out, string(l))
	}
	return out
}

func TestOpenStoreKeepsTheLastLineOfEachJob(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		{"reads an empty file", nil, nil},
		{"keeps one line per job", []string{storeLine("a", "queued"), storeLine("b", "queued")},
			[]string{storeLine("a", "queued"), storeLine("b", "queued")}},
		{"keeps the last line of a job with three lines", []string{storeLine("a", "queued"), storeLine("a", "running"), storeLine("a", "complete")},
			[]string{storeLine("a", "complete")}},
		{"keeps the order of the first line of each job", []string{storeLine("a", "queued"), storeLine("b", "queued"), storeLine("a", "running")},
			[]string{storeLine("a", "running"), storeLine("b", "queued")}},
		{"skips a blank line", []string{storeLine("a", "queued"), "", storeLine("a", "running")},
			[]string{storeLine("a", "running")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := OpenStore(writeStoreFile(t, tt.lines...))
			if err != nil {
				t.Fatal(err)
			}
			if got := lineStrings(s); !slices.Equal(got, tt.want) {
				t.Errorf("Lines() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenStoreMakesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Lines()) != 0 {
		t.Errorf("Lines() = %q, want none", lineStrings(s))
	}
	if err := s.Append("a", json.RawMessage(storeLine("a", "queued"))); err != nil {
		t.Fatal(err)
	}
	if got := fileLines(t, path); !slices.Equal(got, []string{storeLine("a", "queued")}) {
		t.Errorf("file lines = %q, want the appended line", got)
	}
}

func TestOpenStoreSkipsACorruptLineAndLogsItsNumber(t *testing.T) {
	tests := []struct {
		name    string
		corrupt string
	}{
		{"skips a line that is not JSON", `{"id":"b","state":`},
		{"skips a line with no id", `{"state":"queued"}`},
		{"skips a line with an id that is not a string", `{"id":7,"state":"queued"}`},
		{"skips a JSON value that is not an object", `["b"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })

			s, err := OpenStore(writeStoreFile(t, storeLine("a", "queued"), tt.corrupt, storeLine("c", "queued")))
			if err != nil {
				t.Fatalf("OpenStore() returned %v, want the server to start", err)
			}
			want := []string{storeLine("a", "queued"), storeLine("c", "queued")}
			if got := lineStrings(s); !slices.Equal(got, want) {
				t.Errorf("Lines() = %q, want %q", got, want)
			}
			if !strings.Contains(logs.String(), "line=2") {
				t.Errorf("log = %q, want the line number 2", logs.String())
			}
		})
	}
}

func TestAppendAddsALineAndTheLastLineWins(t *testing.T) {
	path := writeStoreFile(t, storeLine("a", "queued"))
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append("a", json.RawMessage(storeLine("a", "running"))); err != nil {
		t.Fatal(err)
	}
	if got := fileLines(t, path); !slices.Equal(got, []string{storeLine("a", "queued"), storeLine("a", "running")}) {
		t.Errorf("file lines = %q, want the two lines", got)
	}
	if got := lineStrings(s); !slices.Equal(got, []string{storeLine("a", "running")}) {
		t.Errorf("Lines() = %q, want the running line", got)
	}
}

func TestStoreRewritesTheFileAbove10LinesPerJob(t *testing.T) {
	tests := []struct {
		name string
		// lines is the line count of the file after the appends.
		lines     int
		wantLines int
	}{
		{"keeps 10 lines of one job", 10, 10},
		{"rewrites 11 lines of one job to one line", 11, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.jsonl")
			s, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			for i := range tt.lines {
				if err := s.Append("a", json.RawMessage(fmt.Sprintf(`{"id":"a","n":%d}`, i))); err != nil {
					t.Fatal(err)
				}
			}
			got := fileLines(t, path)
			if len(got) != tt.wantLines {
				t.Fatalf("file has %d lines, want %d", len(got), tt.wantLines)
			}
			if last := fmt.Sprintf(`{"id":"a","n":%d}`, tt.lines-1); got[len(got)-1] != last {
				t.Errorf("last file line = %q, want %q", got[len(got)-1], last)
			}
		})
	}
}

func TestStoreAppendsAfterARewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 11 {
		if err := s.Append("a", json.RawMessage(fmt.Sprintf(`{"id":"a","n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Append("b", json.RawMessage(storeLine("b", "queued"))); err != nil {
		t.Fatal(err)
	}
	want := []string{`{"id":"a","n":10}`, storeLine("b", "queued")}
	if got := fileLines(t, path); !slices.Equal(got, want) {
		t.Errorf("file lines = %q, want %q", got, want)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := lineStrings(reopened); !slices.Equal(got, want) {
		t.Errorf("Lines() after a reopen = %q, want %q", got, want)
	}
}

func TestOpenStoreRewritesAFileAbove10LinesPerJob(t *testing.T) {
	var lines []string
	for i := range 11 {
		lines = append(lines, fmt.Sprintf(`{"id":"a","n":%d}`, i))
	}
	path := writeStoreFile(t, lines...)
	if _, err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	if got := fileLines(t, path); !slices.Equal(got, []string{`{"id":"a","n":10}`}) {
		t.Errorf("file lines = %q, want one line", got)
	}
}
