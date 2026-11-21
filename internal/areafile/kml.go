package areafile

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/paulmach/orb"
)

// kmlGeometry holds the geometry elements of a Placemark or a MultiGeometry (OGC KML 2.2).
// A MultiGeometry can hold another MultiGeometry, so the type holds itself.
type kmlGeometry struct {
	Polygons []kmlPolygon  `xml:"Polygon"`
	Lines    []kmlLine     `xml:"LineString"`
	Points   []struct{}    `xml:"Point"`
	Multi    []kmlGeometry `xml:"MultiGeometry"`
}

type kmlPlacemark struct {
	Name string `xml:"name"`
	kmlGeometry
}

type kmlLine struct {
	Coordinates string `xml:"coordinates"`
}

type kmlPolygon struct {
	Outer string   `xml:"outerBoundaryIs>LinearRing>coordinates"`
	Inner []string `xml:"innerBoundaryIs>LinearRing>coordinates"`
}

// parseKML adds one polygon area and one route_buffer area per Placemark, for the parts that the Placemark holds.
// A Placemark can sit at any depth of Document and Folder elements, so the decoder walks every element.
func parseKML(data []byte, d Defaults) (Result, error) {
	var res Result
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return res, nil
		}
		if err != nil {
			return Result{}, fmt.Errorf("read the KML: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "Placemark" {
			continue
		}
		var p kmlPlacemark
		if err := dec.DecodeElement(&p, &start); err != nil {
			return Result{}, fmt.Errorf("read the KML: %w", err)
		}
		if err := res.addPlacemark(p, d); err != nil {
			return Result{}, fmt.Errorf("read the Placemark %q: %w", p.Name, err)
		}
	}
}

func (r *Result) addPlacemark(p kmlPlacemark, d Defaults) error {
	var parts kmlParts
	if err := parts.collect(p.kmlGeometry); err != nil {
		return err
	}
	r.Points += parts.points
	switch len(parts.polygons) {
	case 0:
	case 1:
		r.add(parts.polygons[0], p.Name, d)
	default:
		r.add(parts.polygons, p.Name, d)
	}
	switch len(parts.lines) {
	case 0:
	case 1:
		r.add(parts.lines[0], p.Name, d)
	default:
		r.add(parts.lines, p.Name, d)
	}
	return nil
}

// kmlParts holds the polygons, the lines and the Point count of one Placemark, over every level of MultiGeometry.
type kmlParts struct {
	polygons orb.MultiPolygon
	lines    orb.MultiLineString
	points   int
}

func (p *kmlParts) collect(g kmlGeometry) error {
	p.points += len(g.Points)
	for _, kp := range g.Polygons {
		polygon, err := kmlPolygonOf(kp)
		if err != nil {
			return err
		}
		p.polygons = append(p.polygons, polygon)
	}
	for _, kl := range g.Lines {
		line, err := kmlPositions(kl.Coordinates)
		if err != nil {
			return err
		}
		p.lines = append(p.lines, line)
	}
	for _, m := range g.Multi {
		if err := p.collect(m); err != nil {
			return err
		}
	}
	return nil
}

func kmlPolygonOf(kp kmlPolygon) (orb.Polygon, error) {
	outer, err := kmlPositions(kp.Outer)
	if err != nil {
		return nil, err
	}
	polygon := orb.Polygon{orb.Ring(outer)}
	for _, inner := range kp.Inner {
		ring, err := kmlPositions(inner)
		if err != nil {
			return nil, err
		}
		polygon = append(polygon, orb.Ring(ring))
	}
	return polygon, nil
}

// kmlPositions returns the positions of a KML coordinates element.
// KML 2.2 separates the tuples with white space, and each tuple is lon,lat[,alt] with no space, so the altitude is dropped.
func kmlPositions(text string) (orb.LineString, error) {
	tuples := strings.Fields(text)
	line := make(orb.LineString, 0, len(tuples))
	for _, tuple := range tuples {
		parts := strings.Split(tuple, ",")
		if len(parts) < 2 {
			return nil, fmt.Errorf("position %q has %d numbers, and it needs at least 2", tuple, len(parts))
		}
		lon, err := strconv.ParseFloat(parts[0], 64)
		if err != nil {
			return nil, fmt.Errorf("position %q: longitude: %w", tuple, err)
		}
		lat, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return nil, fmt.Errorf("position %q: latitude: %w", tuple, err)
		}
		line = append(line, orb.Point{lon, lat})
	}
	return line, nil
}

// parseKMZ reads the KML of a KMZ archive: doc.kml, or else the first .kml file in the archive (feature 11, Edge cases).
func parseKMZ(data []byte, d Defaults) (Result, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Result{}, fmt.Errorf("read the KMZ: %w", err)
	}
	var kml *zip.File
	for _, f := range zr.File {
		if f.Name == "doc.kml" {
			kml = f
			break
		}
		if kml == nil && strings.HasSuffix(strings.ToLower(f.Name), ".kml") {
			kml = f
		}
	}
	if kml == nil {
		return Result{}, errors.New("read the KMZ: the archive holds no .kml file")
	}
	rc, err := kml.Open()
	if err != nil {
		return Result{}, fmt.Errorf("read the KMZ: %w", err)
	}
	defer func() { _ = rc.Close() }() // The file is read only, so a close error loses no data.
	// The archive is compressed, so the KML in it gets the size limit again.
	text, err := readLimited(rc)
	if err != nil {
		return Result{}, err
	}
	return parseKML(text, d)
}
