// Package xmlparser parses XML map source configurations in MOBAC format.
package xmlparser

import (
	"encoding/xml"
	"errors"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// MapSource represents a map tile source configuration.
type MapSource struct {
	Name            string
	URL             string
	MinZoom         int
	MaxZoom         int
	TileType        string
	TileUpdate      string
	BackgroundColor string
	IgnoreErrors    bool
	ServerParts     []string
	// InvertYCoordinate makes FormatURL send the TMS row as {$y} (FR-source-sync-21).
	InvertYCoordinate bool
	// Kind is KindTile. Issue #41 adds the WMS kind.
	Kind string
	// Origin is OriginSnapshot or OriginUser.
	Origin string
}

// KindTile marks a map source with a tile URL template.
const KindTile = "tile"

// The origins of a map source (FR-source-sync-4).
const (
	OriginSnapshot = "snapshot"
	OriginUser     = "user"
)

// FileExtension returns the file extension for this tile type.
func (m *MapSource) FileExtension() string {
	return strings.ToLower(m.TileType)
}

// FormatURL returns the tile URL of the XYZ tile x, y at zoom level z.
// It puts serverPart in {$serverpart}.
// An empty serverPart selects the server part of the tile key (FR-source-sync-23).
// When InvertYCoordinate is true, {$y} is the TMS row (FR-source-sync-21).
// {$q} always encodes the XYZ row, because a quadkey has its origin at the top left.
func (m *MapSource) FormatURL(x, y, z int, serverPart string) string {
	row := y
	if m.InvertYCoordinate {
		row = (1<<z - 1) - y
	}
	if serverPart == "" {
		serverPart = m.GetServerPart(x, y)
	}
	url := m.URL
	url = strings.ReplaceAll(url, "{$x}", strconv.Itoa(x))
	url = strings.ReplaceAll(url, "{$y}", strconv.Itoa(row))
	url = strings.ReplaceAll(url, "{$z}", strconv.Itoa(z))
	if strings.Contains(url, "{$q}") {
		url = strings.ReplaceAll(url, "{$q}", m.tileToQuadkey(x, y, z))
	}
	return strings.ReplaceAll(url, "{$serverpart}", serverPart)
}

// Hosts returns the distinct hosts of the tile URLs, in lower case and with no port.
// It formats the URL once for each server part, because a server part can change the host.
// A URL that does not parse, or that has no host, adds no host.
func (m *MapSource) Hosts() []string {
	parts := m.ServerParts
	if len(parts) == 0 {
		parts = []string{""}
	}
	hosts := []string{}
	seen := map[string]bool{}
	for _, part := range parts {
		u, err := url.Parse(m.FormatURL(0, 0, 0, part))
		if err != nil {
			continue
		}
		h := strings.ToLower(u.Hostname())
		if h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// tileToQuadkey converts tile coordinates to Bing Maps quadkey.
func (m *MapSource) tileToQuadkey(x, y, z int) string {
	var quadkey strings.Builder
	for i := z; i > 0; i-- {
		digit := 0
		mask := 1 << (i - 1)
		if (x & mask) != 0 {
			digit++
		}
		if (y & mask) != 0 {
			digit += 2
		}
		quadkey.WriteString(strconv.Itoa(digit))
	}
	return quadkey.String()
}

// GetServerPart returns the server part of the XYZ tile x, y, which is (x + y) mod len(ServerParts) (FR-source-sync-23).
// A tile thus always uses the same part, whatever the order of the requests is.
// It returns an empty string when the map source has no server parts.
func (m *MapSource) GetServerPart(x, y int) string {
	if len(m.ServerParts) == 0 {
		return ""
	}
	return m.ServerParts[(x+y)%len(m.ServerParts)]
}

// knownPlaceholders are the placeholders of the ATAK-Maps XML reference v2.3.1, section "URL Placeholders" (FR-source-sync-24).
// ATAK replaces them with exact string matches, so the letter case must match.
var knownPlaceholders = map[string]bool{
	"{$z}":          true,
	"{$x}":          true,
	"{$y}":          true,
	"{$q}":          true,
	"{$serverpart}": true,
}

var placeholderPattern = regexp.MustCompile(`\{\$[^}]*\}`)

// unknownPlaceholder returns the first placeholder in url that the XML reference does not document.
// It returns an empty string when url holds only known placeholders.
func unknownPlaceholder(url string) string {
	for _, p := range placeholderPattern.FindAllString(url, -1) {
		if !knownPlaceholders[p] {
			return p
		}
	}
	return ""
}

// The skip reasons of a source file (FR-source-sync-27).
// Issue #41 records SkipUnsupportedCRS and issue #42 records SkipUnknownPlaceholder.
const (
	SkipParseError         = "parse_error"
	SkipMissingName        = "missing_name"
	SkipMissingURL         = "missing_url"
	SkipUnsupportedCRS     = "unsupported_crs"
	SkipUnknownPlaceholder = "unknown_placeholder"
	SkipDuplicateName      = "duplicate_name"
)

// SkipError tells why a source file adds no map source to the source catalog.
type SkipError struct {
	// Reason is one of the Skip constants.
	Reason string
	Detail string
}

func (e *SkipError) Error() string {
	return e.Reason + ": " + e.Detail
}

// SkippedSource is a source file that adds no map source to the source catalog (FR-source-sync-26).
type SkippedSource struct {
	// Path is the path in the snapshot for a snapshot file, and the file path for a user file.
	Path   string
	Reason string
	Detail string
}

// customMapSourceXML is the XML structure for parsing.
// XMLName has no tag, so that parse can name the root element of a file that is not a customMapSource.
type customMapSourceXML struct {
	XMLName         xml.Name
	Name            string `xml:"name"`
	URL             string `xml:"url"`
	MinZoom         int    `xml:"minZoom"`
	MaxZoom         int    `xml:"maxZoom"`
	TileType        string `xml:"tileType"`
	TileUpdate      string `xml:"tileUpdate"`
	BackgroundColor string `xml:"backgroundColor"`
	IgnoreErrors    bool   `xml:"ignoreErrors"`
	ServerParts     string `xml:"serverParts"`
	// InvertYCoordinate is a string, because the XML reference turns the inversion on only for the exact string true.
	InvertYCoordinate string `xml:"invertYCoordinate"`
}

// XMLParser parses map source XML configuration files.
type XMLParser struct {
	MapsourcesDir string
	snapshot      fs.FS
}

// New returns a parser for the user source directory mapsourcesDir alone.
// An empty mapsourcesDir gives a parser with no source directory.
func New(mapsourcesDir string) *XMLParser {
	return &XMLParser{MapsourcesDir: mapsourcesDir}
}

// NewWithSnapshot returns a parser that loads the source files of snapshot first, then the user source directory mapsourcesDir.
// A map source from mapsourcesDir replaces a snapshot map source with the same name (FR-source-sync-5).
func NewWithSnapshot(snapshot fs.FS, mapsourcesDir string) *XMLParser {
	p := New(mapsourcesDir)
	p.snapshot = snapshot
	return p
}

// DiscoverSources returns the map sources of the snapshot and of the user source directory, by name.
// It reads the source files on each call, so it always returns the current files.
// A missing user source directory adds no map source and is not an error.
func (p *XMLParser) DiscoverSources() map[string]*MapSource {
	sources, _ := p.scan()
	return sources
}

// SkippedSources returns the source files that add no map source, in the order that DiscoverSources reads them.
// It returns an empty slice when every source file loads.
func (p *XMLParser) SkippedSources() []SkippedSource {
	_, skipped := p.scan()
	return skipped
}

// scan reads the snapshot, then the user source directory.
// It keeps no state, so concurrent HTTP handlers can call it.
// A full scan of the snapshot takes less than 1 ms, so a cache saves no measurable time.
func (p *XMLParser) scan() (map[string]*MapSource, []SkippedSource) {
	sources := make(map[string]*MapSource)
	skipped := []SkippedSource{}
	// first holds the first file of each name in each directory of one tree (FR-source-sync-31).
	type dirName struct{ origin, dir, name string }
	first := make(map[dirName]string)

	add := func(rel, shown string, source *MapSource, err error) {
		if err != nil {
			var se *SkipError
			if !errors.As(err, &se) {
				se = &SkipError{Reason: SkipParseError, Detail: err.Error()}
			}
			skipped = append(skipped, SkippedSource{Path: shown, Reason: se.Reason, Detail: se.Detail})
			slog.Debug("skipped source file", "file", shown, "reason", se.Reason, "detail", se.Detail)
			return
		}
		key := dirName{source.Origin, path.Dir(rel), source.Name}
		if prev, ok := first[key]; ok {
			skipped = append(skipped, SkippedSource{
				Path:   shown,
				Reason: SkipDuplicateName,
				Detail: "map source " + strconv.Quote(source.Name) + " is also in " + prev,
			})
			return
		}
		first[key] = shown
		// A later tree replaces a map source of the same name, so a user file replaces a snapshot file (FR-source-sync-5).
		sources[source.Name] = source
	}

	if p.snapshot != nil {
		walkXML(p.snapshot, func(rel string) {
			source, err := p.ParseFS(p.snapshot, rel)
			add(rel, rel, source, err)
		})
	}
	if p.MapsourcesDir != "" {
		if _, err := os.Stat(p.MapsourcesDir); err == nil {
			walkXML(os.DirFS(p.MapsourcesDir), func(rel string) {
				full := filepath.Join(p.MapsourcesDir, filepath.FromSlash(rel))
				source, err := p.ParseFile(full)
				add(rel, full, source, err)
			})
		} else {
			slog.Debug("mapsources dir not found", "path", p.MapsourcesDir)
		}
	}
	slog.Debug("loaded map sources", "count", len(sources), "skipped", len(skipped))
	return sources, skipped
}

// walkXML calls load for each .xml file in fsys, at any depth.
// fs.WalkDir reads each directory in lexical order, so the first file of a duplicate name is the first in lexical path order.
func walkXML(fsys fs.FS, load func(path string)) {
	// The callback returns nil for every entry, so WalkDir returns nil.
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(strings.ToLower(path), ".xml") {
			load(path)
		}
		return nil
	})
}

// ParseFile returns the map source of the source file at xmlPath, with origin user.
// It returns a *SkipError when the file holds no valid customMapSource, and the read error when it cannot read the file.
func (p *XMLParser) ParseFile(xmlPath string) (*MapSource, error) {
	data, err := os.ReadFile(xmlPath) //nolint:gosec // The operator chooses the map sources directory.
	if err != nil {
		return nil, err
	}
	return parse(data, OriginUser)
}

// ParseFS returns the map source of the snapshot source file name in fsys, with origin snapshot.
// It returns a *SkipError when the file holds no valid customMapSource, and the read error when it cannot read the file.
func (p *XMLParser) ParseFS(fsys fs.FS, name string) (*MapSource, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	return parse(data, OriginSnapshot)
}

func parse(data []byte, origin string) (*MapSource, error) {
	var raw customMapSourceXML
	if err := xml.Unmarshal(data, &raw); err != nil {
		return nil, &SkipError{Reason: SkipParseError, Detail: err.Error()}
	}
	// TODO(#41): Parse the customWmsMapSource root element.
	if raw.XMLName.Local != "customMapSource" {
		return nil, &SkipError{Reason: SkipParseError, Detail: "root element <" + raw.XMLName.Local + "> is not <customMapSource>"}
	}

	raw.Name = strings.TrimSpace(raw.Name)
	raw.URL = strings.TrimSpace(raw.URL)
	if raw.Name == "" {
		return nil, &SkipError{Reason: SkipMissingName, Detail: "the <name> element is empty or missing"}
	}
	if raw.URL == "" {
		return nil, &SkipError{Reason: SkipMissingURL, Detail: "the <url> element is empty or missing"}
	}

	if p := unknownPlaceholder(raw.URL); p != "" {
		return nil, &SkipError{Reason: SkipUnknownPlaceholder, Detail: "the <url> element holds the unknown placeholder " + p}
	}

	if raw.MinZoom == 0 && raw.MaxZoom == 0 {
		raw.MaxZoom = 18
	}
	if raw.TileType == "" {
		raw.TileType = "png"
	}
	if raw.TileUpdate == "" {
		raw.TileUpdate = "None"
	}
	if raw.BackgroundColor == "" {
		raw.BackgroundColor = "#000000"
	}

	// ATAK splits on whitespace only, but FR-source-sync-22 also splits on commas, so "a,b,c" gives three parts.
	serverParts := strings.FieldsFunc(raw.ServerParts, func(r rune) bool { return unicode.IsSpace(r) || r == ',' })
	if serverParts == nil {
		serverParts = []string{}
	}

	return &MapSource{
		Name:            raw.Name,
		URL:             raw.URL,
		MinZoom:         raw.MinZoom,
		MaxZoom:         raw.MaxZoom,
		TileType:        raw.TileType,
		TileUpdate:      raw.TileUpdate,
		BackgroundColor: raw.BackgroundColor,
		IgnoreErrors:    raw.IgnoreErrors,
		ServerParts:     serverParts,
		// A source file can put white space around the value, and the XML decoder keeps it.
		InvertYCoordinate: strings.TrimSpace(raw.InvertYCoordinate) == "true",
		Kind:              KindTile,
		Origin:            origin,
	}, nil
}

// GetSource returns a specific map source by name.
func (p *XMLParser) GetSource(name string) *MapSource {
	sources := p.DiscoverSources()
	return sources[name]
}

// ListSources returns sorted source names.
func (p *XMLParser) ListSources() []string {
	sources := p.DiscoverSources()
	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	// Simple sort
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[i] > names[j] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

// ValidateSource validates a map source configuration.
func (p *XMLParser) ValidateSource(source *MapSource) []string {
	var errors []string
	if source.Name == "" {
		errors = append(errors, "source name is required")
	}
	if source.URL == "" {
		errors = append(errors, "source URL is required")
	}
	if source.MinZoom < 0 || source.MinZoom > 25 {
		errors = append(errors, "invalid min_zoom: "+strconv.Itoa(source.MinZoom))
	}
	if source.MaxZoom < 0 || source.MaxZoom > 25 {
		errors = append(errors, "invalid max_zoom: "+strconv.Itoa(source.MaxZoom))
	}
	if source.MinZoom > source.MaxZoom {
		errors = append(errors, "min_zoom cannot be greater than max_zoom")
	}
	validTypes := map[string]bool{"png": true, "jpg": true, "jpeg": true, "webp": true}
	if !validTypes[strings.ToLower(source.TileType)] {
		errors = append(errors, "unsupported tile type: "+source.TileType)
	}

	hasStandard := strings.Contains(source.URL, "{$x}") &&
		strings.Contains(source.URL, "{$y}") &&
		strings.Contains(source.URL, "{$z}")
	hasBing := strings.Contains(source.URL, "{$q}")
	if !hasStandard && !hasBing {
		errors = append(errors, "URL must contain either {$x}, {$y}, {$z} or {$q} for Bing format")
	}
	return errors
}
