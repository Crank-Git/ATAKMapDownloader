package xmlparser

import (
	"cmp"
	"net/url"
	"strconv"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// readWMS copies the WMS elements of raw to m and makes m a KindWMS map source (FR-source-sync-18).
// The defaults are those of the ATAK-Maps XML reference v2.3.1: version 1.1.1 and CRS EPSG:4326.
// It returns a *SkipError when raw has no layers, because a GetMap request without LAYERS is not valid.
func (m *MapSource) readWMS(raw *customMapSourceXML) error {
	m.Kind = KindWMS
	m.WMSLayers = strings.TrimSpace(raw.Layers)
	if m.WMSLayers == "" {
		return &SkipError{Reason: SkipParseError, Detail: "the <layers> element of a <customWmsMapSource> is empty or missing"}
	}
	m.WMSVersion = cmp.Or(strings.TrimSpace(raw.Version), "1.1.1")
	m.WMSStyles = strings.TrimSpace(raw.Styles)
	m.WMSCRS = cmp.Or(strings.TrimSpace(raw.CoordinateSystem), "EPSG:4326")
	m.WMSParams = strings.TrimSpace(cmp.Or(raw.AditionalParameters, raw.AdditionalParameters))
	return nil
}

// getMapURL returns the WMS GetMap URL of the XYZ tile x, y at zoom level z (FR-source-sync-19).
// WMS 1.3.0 orders BBOX by the axes of the CRS, and EPSG:3857 has easting first, so BBOX is minx,miny,maxx,maxy.
// WMS 1.1.1 names the CRS parameter SRS and always orders BBOX as x, y.
func (m *MapSource) getMapURL(x, y, z int) string {
	minX, minY, maxX, maxY := tilecalc.MercatorBounds(x, y, z)
	bbox := make([]string, 0, 4)
	for _, v := range []float64{minX, minY, maxX, maxY} {
		bbox = append(bbox, strconv.FormatFloat(v, 'f', -1, 64))
	}
	crsName := "SRS"
	if strings.HasPrefix(m.WMSVersion, "1.3") {
		crsName = "CRS"
	}
	return m.wmsURL([][2]string{
		{"SERVICE", "WMS"},
		{"REQUEST", "GetMap"},
		{"VERSION", m.WMSVersion},
		{"LAYERS", m.WMSLayers},
		{"STYLES", m.WMSStyles},
		{crsName, CRSWebMercator},
		{"BBOX", strings.Join(bbox, ",")},
		{"WIDTH", "256"},
		{"HEIGHT", "256"},
		{"FORMAT", m.wmsFormat()},
	}) + m.paramsSuffix()
}

// CapabilitiesURL returns the URL of the WMS GetCapabilities request of a KindWMS map source.
func (m *MapSource) CapabilitiesURL() string {
	return m.wmsURL([][2]string{
		{"SERVICE", "WMS"},
		{"REQUEST", "GetCapabilities"},
		{"VERSION", m.WMSVersion},
	})
}

// wmsFormat returns the FORMAT MIME type of the tile type, as the XML reference maps it.
func (m *MapSource) wmsFormat() string {
	switch t := strings.ToLower(m.TileType); t {
	case "jpg", "jpeg":
		return "image/jpeg"
	default:
		return "image/" + t
	}
}

// paramsSuffix returns WMSParams with a leading &, so that a value without one does not join the last parameter.
func (m *MapSource) paramsSuffix() string {
	if m.WMSParams == "" || strings.HasPrefix(m.WMSParams, "&") {
		return m.WMSParams
	}
	return "&" + m.WMSParams
}

// wmsEscaper keeps ":" and "," of an encoded query value, because RFC 3986 allows them in a query.
// A CRS or BBOX value then stays readable in a log line.
var wmsEscaper = strings.NewReplacer("%3A", ":", "%2C", ",")

// wmsURL returns the base URL with params appended.
// The URL omits a parameter that the base URL or WMSParams holds, because some servers reject a repeated parameter.
// WMS parameter names are case-insensitive, so the comparison ignores the letter case.
func (m *MapSource) wmsURL(params [][2]string) string {
	base, query, _ := strings.Cut(m.URL, "?")
	present := map[string]bool{}
	for _, kv := range strings.Split(query+"&"+m.WMSParams, "&") {
		name, _, _ := strings.Cut(kv, "=")
		present[strings.ToUpper(name)] = true
	}

	var b strings.Builder
	b.WriteString(base)
	b.WriteByte('?')
	b.WriteString(query)
	sep := query != "" && !strings.HasSuffix(query, "&")
	for _, p := range params {
		if present[p[0]] {
			continue
		}
		if sep {
			b.WriteByte('&')
		}
		sep = true
		b.WriteString(p[0])
		b.WriteByte('=')
		b.WriteString(wmsEscaper.Replace(url.QueryEscape(p[1])))
	}
	return b.String()
}
