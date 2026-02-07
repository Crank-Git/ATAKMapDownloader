// Package xmlparser parses XML map source configurations in MOBAC format.
package xmlparser

import (
	"encoding/xml"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MapSource represents a map tile source configuration.
type MapSource struct {
	Name           string
	URL            string
	MinZoom        int
	MaxZoom        int
	TileType       string
	TileUpdate     string
	BackgroundColor string
	IgnoreErrors   bool
	ServerParts    []string
}

// FileExtension returns the file extension for this tile type.
func (m *MapSource) FileExtension() string {
	return strings.ToLower(m.TileType)
}

// FormatURL formats the URL template with tile coordinates.
func (m *MapSource) FormatURL(x, y, z int, serverPart string) string {
	url := m.URL
	url = strings.ReplaceAll(url, "{$x}", strconv.Itoa(x))
	url = strings.ReplaceAll(url, "{$y}", strconv.Itoa(y))
	url = strings.ReplaceAll(url, "{$z}", strconv.Itoa(z))

	if strings.Contains(url, "{$q}") {
		url = strings.ReplaceAll(url, "{$q}", m.tileToQuadkey(x, y, z))
	}

	if serverPart != "" && strings.Contains(url, "{$serverpart}") {
		url = strings.ReplaceAll(url, "{$serverpart}", serverPart)
	} else if len(m.ServerParts) > 0 && strings.Contains(url, "{$serverpart}") {
		url = strings.ReplaceAll(url, "{$serverpart}", m.ServerParts[0])
	}

	return url
}

// tileToQuadkey converts tile coordinates to Bing Maps quadkey.
func (m *MapSource) tileToQuadkey(x, y, z int) string {
	var quadkey strings.Builder
	for i := z; i > 0; i-- {
		digit := 0
		mask := 1 << (i - 1)
		if (x & mask) != 0 {
			digit += 1
		}
		if (y & mask) != 0 {
			digit += 2
		}
		quadkey.WriteString(strconv.Itoa(digit))
	}
	return quadkey.String()
}

// GetServerPart returns a server part for load balancing.
func (m *MapSource) GetServerPart(tileIndex int) string {
	if len(m.ServerParts) == 0 {
		return ""
	}
	return m.ServerParts[tileIndex%len(m.ServerParts)]
}

// customMapSourceXML is the XML structure for parsing.
type customMapSourceXML struct {
	XMLName         xml.Name `xml:"customMapSource"`
	Name            string   `xml:"name"`
	URL             string   `xml:"url"`
	MinZoom         int      `xml:"minZoom"`
	MaxZoom         int      `xml:"maxZoom"`
	TileType        string   `xml:"tileType"`
	TileUpdate      string   `xml:"tileUpdate"`
	BackgroundColor string   `xml:"backgroundColor"`
	IgnoreErrors    bool     `xml:"ignoreErrors"`
	ServerParts     string   `xml:"serverParts"`
}

// XMLParser parses map source XML configuration files.
type XMLParser struct {
	MapsourcesDir  string
	sourcesCache   map[string]*MapSource
	lastScanTime   time.Time
}

// New creates a new XMLParser.
func New(mapsourcesDir string) *XMLParser {
	return &XMLParser{
		MapsourcesDir: mapsourcesDir,
		sourcesCache:  make(map[string]*MapSource),
	}
}

// DiscoverSources discovers and parses all XML source files.
func (p *XMLParser) DiscoverSources(forceRescan bool) map[string]*MapSource {
	info, err := os.Stat(p.MapsourcesDir)
	if err != nil {
		slog.Debug("mapsources dir not found", "path", p.MapsourcesDir)
		return p.sourcesCache
	}

	currentTime := info.ModTime()
	if !forceRescan && len(p.sourcesCache) > 0 && !currentTime.After(p.lastScanTime) {
		return p.sourcesCache
	}

	slog.Info("scanning for XML sources", "dir", p.MapsourcesDir)
	sources := make(map[string]*MapSource)

	filepath.WalkDir(p.MapsourcesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".xml") {
			return nil
		}
		source, err := p.ParseFile(path)
		if err != nil {
			// Skip unsupported formats (e.g. customWmsMapSource) silently
			if !strings.Contains(err.Error(), "customMapSource") {
				slog.Debug("skipped XML", "file", path, "reason", err.Error())
			}
			return nil
		}
		if source != nil {
			sources[source.Name] = source
			slog.Debug("loaded source", "name", source.Name)
		}
		return nil
	})

	p.sourcesCache = sources
	p.lastScanTime = currentTime
	slog.Info("loaded map sources", "count", len(sources))
	return sources
}

// ParseFile parses a single XML configuration file.
func (p *XMLParser) ParseFile(xmlPath string) (*MapSource, error) {
	data, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil, err
	}

	var raw customMapSourceXML
	if err := xml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	raw.Name = strings.TrimSpace(raw.Name)
	raw.URL = strings.TrimSpace(raw.URL)
	if raw.Name == "" || raw.URL == "" {
		slog.Warn("missing required fields", "file", xmlPath)
		return nil, nil
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

	serverParts := []string{}
	if raw.ServerParts != "" {
		for _, part := range strings.Fields(raw.ServerParts) {
			if s := strings.TrimSpace(part); s != "" {
				serverParts = append(serverParts, s)
			}
		}
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
	}, nil
}

// GetSource returns a specific map source by name.
func (p *XMLParser) GetSource(name string) *MapSource {
	sources := p.DiscoverSources(false)
	return sources[name]
}

// ListSources returns sorted source names.
func (p *XMLParser) ListSources() []string {
	sources := p.DiscoverSources(false)
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
