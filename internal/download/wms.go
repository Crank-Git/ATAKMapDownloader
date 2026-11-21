package download

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

// ErrUnsupportedCRS is the failure reason of a WMS job whose server does not list EPSG:3857 for the layers (FR-source-sync-20).
var ErrUnsupportedCRS = errors.New("unsupported_crs")

// maxCapabilitiesBytes limits the GetCapabilities response that the engine reads.
// A server with many layers sends a document of some MB, so 32 MiB leaves a wide margin.
const maxCapabilitiesBytes = 32 << 20

// crsCache holds, for each WMS map source, whether its server lists EPSG:3857 for its layers.
// The engine reads the GetCapabilities document one time for each map source (FR-source-sync-20).
type crsCache struct {
	mu      sync.Mutex
	offered map[string]bool
}

// wmsCRS is shared by every job of the process, so a second job of a map source sends no GetCapabilities request.
var wmsCRS = &crsCache{offered: map[string]bool{}}

// checkCRS returns nil when the GetMap requests of source can use EPSG:3857.
// A tile map source and a WMS source file that declares EPSG:3857 need no check.
// Otherwise it reads the GetCapabilities document of the server through the job gate, so the policy table limits the request.
// It returns ErrUnsupportedCRS when the server does not list EPSG:3857 for every layer of source.
// It returns another error when the request or the parse fails, and it does not cache that result.
func (e *Engine) checkCRS(ctx context.Context, jg *jobGate, source *xmlparser.MapSource) error {
	if source.Kind != xmlparser.KindWMS || strings.EqualFold(source.WMSCRS, xmlparser.CRSWebMercator) {
		return nil
	}
	capURL := source.CapabilitiesURL()
	// The layers are part of the key, because two map sources can use one server with different layers.
	key := capURL + "\n" + source.WMSLayers
	wmsCRS.mu.Lock()
	offered, ok := wmsCRS.offered[key]
	wmsCRS.mu.Unlock()
	if !ok {
		doc, err := e.capabilities(ctx, jg, capURL)
		if err != nil {
			return err
		}
		if offered, err = offersEPSG3857(doc, source.WMSLayers); err != nil {
			return err
		}
		wmsCRS.mu.Lock()
		wmsCRS.offered[key] = offered
		wmsCRS.mu.Unlock()
	}
	if !offered {
		return fmt.Errorf("%w: the WMS server does not list %s for the layers %q, and the source file declares %s",
			ErrUnsupportedCRS, xmlparser.CRSWebMercator, source.WMSLayers, source.WMSCRS)
	}
	return nil
}

// capabilities returns the GetCapabilities document at capURL.
// It returns an error when the request fails or the status is not 200.
func (e *Engine) capabilities(ctx context.Context, jg *jobGate, capURL string) ([]byte, error) {
	if err := jg.acquire(ctx); err != nil {
		return nil, err
	}
	defer jg.host.release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, capURL, nil)
	if err != nil {
		return nil, fmt.Errorf("make the GetCapabilities request: %w", err)
	}
	req.Header.Set("User-Agent", e.cfg.UserAgent)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send the GetCapabilities request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // The body is read before the close, so a close error loses nothing.
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the WMS server returned status %d for GetCapabilities", resp.StatusCode)
	}
	doc, err := io.ReadAll(io.LimitReader(resp.Body, maxCapabilitiesBytes))
	if err != nil {
		return nil, fmt.Errorf("read the GetCapabilities response: %w", err)
	}
	return doc, nil
}

// capLayer is one Layer element of a capabilities document.
// WMS 1.3.0 names the CRS element CRS, and WMS 1.1.1 names it SRS.
// The tags have no namespace, so they match the elements of both versions.
type capLayer struct {
	Name   string     `xml:"Name"`
	CRS    []string   `xml:"CRS"`
	SRS    []string   `xml:"SRS"`
	Layers []capLayer `xml:"Layer"`
}

// offersEPSG3857 reports whether the capabilities document doc lists EPSG:3857 for every layer in the comma-separated list layers.
// A layer inherits the CRS list of its parent layer (OGC WMS 1.3.0, 7.2.4.6.7).
// A layer that the document does not list makes the result false.
func offersEPSG3857(doc []byte, layers string) (bool, error) {
	var caps struct {
		Layers []capLayer `xml:"Capability>Layer"`
	}
	if err := xml.Unmarshal(doc, &caps); err != nil {
		return false, fmt.Errorf("parse the GetCapabilities response: %w", err)
	}
	offered := map[string]bool{}
	var walk func(l capLayer, inherited bool)
	walk = func(l capLayer, inherited bool) {
		has := inherited
		// WMS 1.1.0 lets one SRS element hold a list, so the engine splits each value on white space.
		for _, v := range append(l.CRS, l.SRS...) {
			for _, crs := range strings.Fields(v) {
				has = has || strings.EqualFold(crs, xmlparser.CRSWebMercator)
			}
		}
		if name := strings.TrimSpace(l.Name); name != "" {
			offered[name] = offered[name] || has
		}
		for _, child := range l.Layers {
			walk(child, has)
		}
	}
	for _, l := range caps.Layers {
		walk(l, false)
	}
	for _, name := range strings.Split(layers, ",") {
		if !offered[strings.TrimSpace(name)] {
			return false, nil
		}
	}
	return true, nil
}
