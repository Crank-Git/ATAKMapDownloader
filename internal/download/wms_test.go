package download

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

// capabilitiesWith3857 lists EPSG:3857 on the root layer, so layer 12 inherits it.
const capabilitiesWith3857 = `<?xml version="1.0" encoding="UTF-8"?>
<WMS_Capabilities version="1.3.0" xmlns="http://www.opengis.net/wms">
  <Capability>
    <Layer>
      <Title>NFHL</Title>
      <CRS>EPSG:4326</CRS>
      <CRS>EPSG:3857</CRS>
      <Layer><Name>12</Name><Title>Flood Hazard Zones</Title></Layer>
    </Layer>
  </Capability>
</WMS_Capabilities>`

// capabilitiesWithout3857 lists EPSG:4326 alone.
const capabilitiesWithout3857 = `<?xml version="1.0" encoding="UTF-8"?>
<WMS_Capabilities version="1.3.0" xmlns="http://www.opengis.net/wms">
  <Capability>
    <Layer>
      <Title>NFHL</Title>
      <CRS>EPSG:4326</CRS>
      <Layer><Name>12</Name><Title>Flood Hazard Zones</Title></Layer>
    </Layer>
  </Capability>
</WMS_Capabilities>`

// fakeWMS is an httptest WMS server. It answers GetCapabilities with capabilities and GetMap with tile.
type fakeWMS struct {
	*httptest.Server
	capabilities string
	tile         []byte

	mu            sync.Mutex
	capRequests   int
	getMapQueries []url.Values
}

func newFakeWMS(t *testing.T, capabilities string, tile []byte) *fakeWMS {
	t.Helper()
	f := &fakeWMS{capabilities: capabilities, tile: tile}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		defer f.mu.Unlock()
		switch q.Get("REQUEST") {
		case "GetCapabilities":
			f.capRequests++
			w.Header().Set("Content-Type", "text/xml")
			_, _ = w.Write([]byte(f.capabilities))
		case "GetMap":
			f.getMapQueries = append(f.getMapQueries, q)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(f.tile)
		default:
			http.Error(w, "unknown request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeWMS) counts() (int, []url.Values) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.capRequests, append([]url.Values(nil), f.getMapQueries...)
}

// pngTile returns a 256×256 PNG.
func pngTile(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 256, 256))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// femaSource returns the FEMA NFHL source file of the snapshot, pointed at srv.
func femaSource(t *testing.T, srv *fakeWMS) *xmlparser.MapSource {
	t.Helper()
	s, err := xmlparser.ParseFS(os.DirFS("../snapshot"), "maps/overlays/grg_fema_flood.xml")
	if err != nil {
		t.Fatalf("parse the FEMA source file: %v", err)
	}
	s.URL = srv.URL + "/arcgis/services/public/NFHLWMS/MapServer/WMSServer?"
	return s
}

// bboxOf returns the four numbers of a BBOX value.
func bboxOf(t *testing.T, v string) [4]float64 {
	t.Helper()
	parts := strings.Split(v, ",")
	if len(parts) != 4 {
		t.Fatalf("BBOX = %q, want four numbers", v)
	}
	var b [4]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			t.Fatalf("BBOX = %q: %v", v, err)
		}
		b[i] = f
	}
	return b
}

func TestEngineRequestsEPSG3857TilesFromAWMSServerThatListsIt(t *testing.T) {
	srv := newFakeWMS(t, capabilitiesWith3857, pngTile(t))
	source := femaSource(t, srv)

	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, area), filepath.Join(t.TempDir(), "out.sqlite"), nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	caps, queries := srv.counts()
	if caps != 1 {
		t.Errorf("server got %d GetCapabilities requests, want 1", caps)
	}
	if len(queries) != area.TileCount() {
		t.Fatalf("server got %d GetMap requests, want %d", len(queries), area.TileCount())
	}

	// Each tile of the area is a BBOX, so the set of BBOX values shows that every request matches its tile.
	want := map[[4]float64]bool{}
	for pair := range area.IterTiles(t.Context()) {
		minX, minY, maxX, maxY := tilecalc.MercatorBounds(pair[0], pair[1], area.Zoom)
		want[[4]float64{minX, minY, maxX, maxY}] = true
	}
	for _, q := range queries {
		for k, v := range map[string]string{"SERVICE": "WMS", "REQUEST": "GetMap", "LAYERS": "12", "CRS": "EPSG:3857"} {
			if q.Get(k) != v {
				t.Errorf("GetMap %s = %q, want %q", k, q.Get(k), v)
			}
		}
		got := bboxOf(t, q.Get("BBOX"))
		matched := false
		for w := range want {
			if math.Abs(got[0]-w[0]) < 1e-6 && math.Abs(got[1]-w[1]) < 1e-6 && math.Abs(got[2]-w[2]) < 1e-6 && math.Abs(got[3]-w[3]) < 1e-6 {
				delete(want, w)
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("GetMap BBOX = %q matches no tile of the area that has no request yet", q.Get("BBOX"))
		}
	}
}

func TestEngineReadsTheCapabilitiesOfAWMSMapSourceOneTime(t *testing.T) {
	srv := newFakeWMS(t, capabilitiesWith3857, pngTile(t))
	source := femaSource(t, srv)
	e := politeEngine(testConfig(), 1e6)

	for run := range 2 {
		res, err := e.DownloadTiles(t.Context(), source, areasOf(t, area), filepath.Join(t.TempDir(), "out.sqlite"), nil)
		if !res.OK || err != nil {
			t.Fatalf("run %d: DownloadTiles = %v, %v; want true, nil", run, res.OK, err)
		}
	}
	if caps, _ := srv.counts(); caps != 1 {
		t.Errorf("server got %d GetCapabilities requests in two jobs, want 1", caps)
	}
}

func TestEngineStopsAWMSJobWithUnsupportedCRSBeforeTheFirstTileRequest(t *testing.T) {
	srv := newFakeWMS(t, capabilitiesWithout3857, pngTile(t))
	source := femaSource(t, srv)
	out := filepath.Join(t.TempDir(), "out.sqlite")

	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, area), out, nil)
	if res.OK || !errors.Is(err, ErrUnsupportedCRS) {
		t.Fatalf("DownloadTiles = %v, %v; want false, unsupported_crs", res.OK, err)
	}
	if !strings.HasPrefix(err.Error(), "unsupported_crs") {
		t.Errorf("error = %q, want it to start with unsupported_crs", err)
	}
	if _, queries := srv.counts(); len(queries) != 0 {
		t.Errorf("server got %d GetMap requests, want 0", len(queries))
	}
	if _, statErr := os.Stat(out); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Stat(output file) = %v, want no output file", statErr)
	}
}

func TestEngineSendsNoCapabilitiesRequestForAnEPSG3857SourceFile(t *testing.T) {
	srv := newFakeWMS(t, capabilitiesWithout3857, pngTile(t))
	source, err := xmlparser.ParseFile(filepath.Join("..", "..", "testdata", "sources", "wms.xml"))
	if err != nil {
		t.Fatal(err)
	}
	source.URL = strings.Replace(source.URL, fixtureHost, srv.URL, 1)

	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, area), filepath.Join(t.TempDir(), "out.sqlite"), nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	caps, queries := srv.counts()
	if caps != 0 {
		t.Errorf("server got %d GetCapabilities requests, want 0", caps)
	}
	// The fixture is version 1.1.1, so the CRS goes in SRS.
	for _, q := range queries {
		if q.Get("SRS") != "EPSG:3857" {
			t.Errorf("GetMap SRS = %q, want EPSG:3857", q.Get("SRS"))
		}
	}
}

func TestEngineRejectsAWMSXMLExceptionWithStatus200AsNotImage(t *testing.T) {
	exception := []byte(`<?xml version="1.0"?><ServiceExceptionReport version="1.3.0"><ServiceException code="InvalidCRS"/></ServiceExceptionReport>`)
	srv := newFakeWMS(t, capabilitiesWith3857, exception)
	source := femaSource(t, srv)
	e := politeEngine(testConfig(), 1e6)
	var mu sync.Mutex
	reasons := map[Reason]int{}
	e.onTileFailed = func(_, _, _ int, reason Reason) {
		mu.Lock()
		reasons[reason]++
		mu.Unlock()
	}

	res, err := e.DownloadTiles(t.Context(), source, areasOf(t, area), filepath.Join(t.TempDir(), "out.sqlite"), nil)
	if res.OK || res.Downloaded != 0 {
		t.Fatalf("DownloadTiles = %+v, %v; want a failed job with no downloaded tile", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reasons) != 1 || reasons[ReasonNotImage] == 0 {
		t.Errorf("failure reasons = %v, want not_image only", reasons)
	}
}

func TestOffersEPSG3857ReadsTheCRSOfEachLayer(t *testing.T) {
	tests := []struct {
		name   string
		doc    string
		layers string
		want   bool
	}{
		{"accepts a CRS that the layer inherits", capabilitiesWith3857, "12", true},
		{"rejects a server with EPSG:4326 alone", capabilitiesWithout3857, "12", false},
		{"rejects a layer that the server does not list", capabilitiesWith3857, "12,99", false},
		{
			"accepts the SRS of version 1.1.1", `<WMT_MS_Capabilities version="1.1.1"><Capability><Layer><SRS>EPSG:4326 EPSG:3857</SRS>
<Layer><Name>CBMT</Name></Layer></Layer></Capability></WMT_MS_Capabilities>`, "CBMT", true,
		},
		{
			"rejects a layer when only a sibling lists the CRS", `<WMS_Capabilities><Capability><Layer>
<Layer><Name>a</Name><CRS>EPSG:3857</CRS></Layer><Layer><Name>b</Name><CRS>EPSG:4326</CRS></Layer></Layer></Capability></WMS_Capabilities>`, "a,b", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := offersEPSG3857([]byte(tt.doc), tt.layers)
			if err != nil || got != tt.want {
				t.Errorf("offersEPSG3857 = %v, %v; want %v, nil", got, err, tt.want)
			}
		})
	}
}
