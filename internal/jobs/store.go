package jobs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
)

// compactFactor is the line count per job above which the store rewrites its file (FR-team-server-12).
const compactFactor = 10

// Store keeps the lines of jobs.jsonl, one JSON object per line (FR-team-server-7).
// It knows only the id field of a line, so the job record stays in the server code.
type Store struct {
	mu   sync.Mutex
	path string
	f    *os.File
	// lines is the line count of the file.
	lines int
	// last holds the last line of each job id.
	last map[string]json.RawMessage
	// order holds each job id in the order of its first line, so a rewrite keeps the file order.
	order []string
}

// OpenStore reads the file at path and keeps the last line of each job id (FR-team-server-8).
// It makes the file when it does not exist.
// It skips a corrupt line and logs its line number, so one bad line does not stop the server.
// It rewrites the file when the file has more than 10 lines per job.
// It returns an error when the file cannot be read, made or rewritten.
func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, last: map[string]json.RawMessage{}}
	data, err := os.ReadFile(path) //nolint:gosec // The path is the jobs.jsonl file of the data directory of the operator.
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	// A line holds a job request, and its areas can come from a 5 MiB area file.
	sc.Buffer(nil, 16<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		s.lines++
		id, ok := lineID(line)
		if !ok {
			slog.Warn("skip a corrupt line of jobs.jsonl", "path", path, "line", n)
			continue
		}
		s.keep(id, bytes.Clone(line))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := s.open(); err != nil {
		return nil, err
	}
	if err := s.compactIfLarge(); err != nil {
		return nil, err
	}
	return s, nil
}

// lineID returns the id field of line. It returns false when line is not a JSON object with a string id.
func lineID(line []byte) (string, bool) {
	var v struct {
		ID *string `json:"id"`
	}
	if err := json.Unmarshal(line, &v); err != nil || v.ID == nil {
		return "", false
	}
	return *v.ID, true
}

// keep makes line the last line of the job id.
func (s *Store) keep(id string, line json.RawMessage) {
	if _, ok := s.last[id]; !ok {
		s.order = append(s.order, id)
	}
	s.last[id] = line
}

// open opens the file for appends.
func (s *Store) open() error {
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // See OpenStore.
	if err != nil {
		return fmt.Errorf("open %s: %w", s.path, err)
	}
	s.f = f
	return nil
}

// Lines returns the last line of each job id, in the order of the first line of each id.
func (s *Store) Lines() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]json.RawMessage, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.last[id])
	}
	return out
}

// Append adds line to the file as the last line of the job id.
// line must be one JSON object with no newline.
// It rewrites the file when the file then has more than 10 lines per job.
// It returns the write error.
func (s *Store) Append(id string, line json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.f.Write(append(bytes.Clone(line), '\n')); err != nil {
		return fmt.Errorf("append to %s: %w", s.path, err)
	}
	s.lines++
	s.keep(id, line)
	return s.compactIfLarge()
}

// compactIfLarge rewrites the file with one line per job when it has more than compactFactor lines per job.
// It writes a temporary file and renames it, so a crash leaves the old file or the new file.
func (s *Store) compactIfLarge() error {
	if s.lines <= compactFactor*len(s.last) {
		return nil
	}
	var buf bytes.Buffer
	for _, id := range s.order {
		buf.Write(s.last[id])
		buf.WriteByte('\n')
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("rewrite %s: %w", s.path, err)
	}
	// Windows refuses a rename over an open file, so the store closes it first.
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", s.path, err)
	}
	renameErr := os.Rename(tmp, s.path)
	// The store opens the file after a failed rename too, so the next append still works.
	if err := s.open(); err != nil {
		return err
	}
	if renameErr != nil {
		return fmt.Errorf("rewrite %s: %w", s.path, renameErr)
	}
	s.lines = len(s.order)
	return nil
}
