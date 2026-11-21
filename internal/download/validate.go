package download

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Reason is the failure reason of one failed tile.
// The writer stores it in the reason column of atakdl_failed_tiles (FR-robust-downloads-21).
type Reason string

// The failure reasons of a tile.
const (
	// ReasonEmpty is a tile body of 0 bytes (FR-robust-downloads-16).
	ReasonEmpty Reason = "empty"
	// ReasonNotImage is a tile body with no PNG, JPEG, GIF or WebP signature (FR-robust-downloads-17).
	ReasonNotImage Reason = "not_image"
	// ReasonBlockedImage is a tile whose SHA-256 is in blocked.json (FR-robust-downloads-19).
	ReasonBlockedImage Reason = "blocked_image"
	// ReasonNotFound is a 404 response (FR-robust-downloads-25).
	ReasonNotFound Reason = "not_found"
	// ReasonNetwork is a request that fails before the engine reads a full response.
	ReasonNetwork Reason = "network"
	// ReasonTLS is a tile server whose TLS certificate does not verify.
	ReasonTLS Reason = "tls"
	// ReasonWriteFailed is a tile that the writer cannot write to the output file.
	ReasonWriteFailed Reason = "write_failed"
)

// HTTPReason returns the failure reason http_<status> of a response with an error status.
func HTTPReason(status int) Reason {
	return Reason(fmt.Sprintf("http_%d", status))
}

// TileError is the error of one failed tile. Err is the cause, and it can be nil.
type TileError struct {
	Reason Reason
	Err    error
}

// Error returns the reason, and the cause when the error has one.
func (e *TileError) Error() string {
	if e.Err == nil {
		return string(e.Reason)
	}
	return string(e.Reason) + ": " + e.Err.Error()
}

// Unwrap returns the cause of the tile failure.
func (e *TileError) Unwrap() error {
	return e.Err
}

// reasonOf returns the failure reason in err. It returns "" when err holds no TileError.
func reasonOf(err error) Reason {
	var te *TileError
	if errors.As(err, &te) {
		return te.Reason
	}
	return ""
}

// networkError returns the tile error of a request that fails, with reason tls or network.
func networkError(err error) *TileError {
	var cv *tls.CertificateVerificationError
	if errors.As(err, &cv) {
		return &TileError{Reason: ReasonTLS, Err: err}
	}
	return &TileError{Reason: ReasonNetwork, Err: err}
}

// blockedJSON is a JSON list of the hexadecimal SHA-256 hashes of known placeholder images.
// Add a hash only together with a test fixture of its image.
//
//go:embed blocked.json
var blockedJSON []byte

// blockedHashes is the set of hashes in blockedJSON. A test of blocked.json checks that it loads.
var blockedHashes = mustLoadBlocked(blockedJSON)

// loadBlocked returns the set of SHA-256 hashes in data, a JSON list of hexadecimal strings.
// It returns an error when data is not a JSON list or when an entry is not a SHA-256 hash.
func loadBlocked(data []byte) (map[[sha256.Size]byte]struct{}, error) {
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("read the blocked hash list: %w", err)
	}
	set := make(map[[sha256.Size]byte]struct{}, len(list))
	for _, entry := range list {
		raw, err := hex.DecodeString(entry)
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("blocked hash %q is not a SHA-256 hash", entry)
		}
		set[[sha256.Size]byte(raw)] = struct{}{}
	}
	return set, nil
}

// mustLoadBlocked returns loadBlocked(data). The file is embedded at build time, so an error is a defect.
func mustLoadBlocked(data []byte) map[[sha256.Size]byte]struct{} {
	set, err := loadBlocked(data)
	if err != nil {
		panic(err)
	}
	return set
}

// imageSignatures are the first bytes of the PNG, JPEG and GIF formats.
// WebP has a size field inside its signature, so isImage checks it apart.
var imageSignatures = [][]byte{
	{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'},
	{0xFF, 0xD8, 0xFF},
	[]byte("GIF8"),
}

// isImage reports whether data starts with the PNG, JPEG, GIF or WebP signature (FR-robust-downloads-15).
func isImage(data []byte) bool {
	for _, sig := range imageSignatures {
		if bytes.HasPrefix(data, sig) {
			return true
		}
	}
	return len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
}

// validateTile returns the failure reason of the tile body data, or "" when the engine accepts it.
// blocked is the set of SHA-256 hashes that the engine rejects.
func validateTile(data []byte, blocked map[[sha256.Size]byte]struct{}) Reason {
	switch {
	case len(data) == 0:
		return ReasonEmpty
	case !isImage(data):
		return ReasonNotImage
	}
	if _, ok := blocked[sha256.Sum256(data)]; ok {
		return ReasonBlockedImage
	}
	return ""
}

const (
	// placeholderSample is the number of first tiles of a job that the placeholder check counts (FR-robust-downloads-20).
	placeholderSample = 100
	// placeholderLimit is the largest count of one hash in the sample that gives no warning.
	placeholderLimit = placeholderSample / 2
)

// placeholderCheck counts the SHA-256 hashes of the first tiles of one job.
// The tile goroutines share it, so a mutex holds its fields.
type placeholderCheck struct {
	mu     sync.Mutex
	seen   int
	counts map[[sha256.Size]byte]int
	warned bool
}

// add counts sum when the sample is not full.
// It returns true once, when more than half of the sample shares one hash.
func (p *placeholderCheck) add(sum [sha256.Size]byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warned || p.seen >= placeholderSample {
		return false
	}
	if p.counts == nil {
		p.counts = make(map[[sha256.Size]byte]int)
	}
	p.seen++
	p.counts[sum]++
	if p.counts[sum] > placeholderLimit {
		p.warned = true
	}
	return p.warned
}
