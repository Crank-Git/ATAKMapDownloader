package download

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
)

// hostLog records the host of each request and the tile path that it asks for.
type hostLog struct {
	next http.RoundTripper
	mu   sync.Mutex
	// paths maps each tile path to the hosts that got a request for it.
	paths map[string][]string
}

func (h *hostLog) RoundTrip(req *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.paths[req.URL.Path] = append(h.paths[req.URL.Path], req.URL.Hostname())
	h.mu.Unlock()
	return h.next.RoundTrip(req)
}

// hosts returns the distinct hosts that got a request.
func (h *hostLog) hosts() map[string]bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := map[string]bool{}
	for _, hosts := range h.paths {
		for _, host := range hosts {
			seen[host] = true
		}
	}
	return seen
}

// routeToServer makes e send every request to srv, whatever the host of the URL is.
// The engine then requests hosts such as a.tiles.test, which no DNS server knows.
func routeToServer(t *testing.T, e *Engine, srv *testtiles.Server) *hostLog {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse the server URL: %v", err)
	}
	var d net.Dialer
	log := &hostLog{
		next: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, u.Host)
		}},
		paths: map[string][]string{},
	}
	e.client.Transport = log
	return log
}

// serverPartsEngine returns a test engine that sends the requests of every host to srv.
func serverPartsEngine(t *testing.T, srv *testtiles.Server) (*Engine, *hostLog) {
	t.Helper()
	e := politeEngine(testConfig(), 1e6)
	return e, routeToServer(t, e, srv)
}

func TestEngineSendsTheServerPartsOfACommaListToThreeHosts(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "serverparts.xml", srv)
	e, log := serverPartsEngine(t, srv)

	res, err := e.DownloadTiles(t.Context(), source, areasOf(t, area), filepath.Join(t.TempDir(), "out.sqlite"), nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	got := log.hosts()
	for _, want := range []string{"a.tiles.test", "b.tiles.test", "c.tiles.test"} {
		if !got[want] {
			t.Errorf("hosts = %v, want a request to %s", got, want)
		}
	}
	if len(got) != 3 {
		t.Errorf("hosts = %v, want 3 hosts", got)
	}
}

func TestEngineSendsEachTileToTheServerPartOfItsTileKey(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "serverparts.xml", srv)
	parts := []string{"a", "b", "c"}

	// Two jobs download the tiles in a different order, so a counter would give a tile another part.
	for run := range 2 {
		e, log := serverPartsEngine(t, srv)
		e.cfg.MaxConcurrent = 1 + run*4
		res, err := e.DownloadTiles(t.Context(), source, areasOf(t, area), filepath.Join(t.TempDir(), "out.sqlite"), nil)
		if !res.OK || err != nil {
			t.Fatalf("run %d: DownloadTiles = %v, %v; want true, nil", run, res.OK, err)
		}
		for pair := range area.IterTiles(t.Context()) {
			x, y := pair[0], pair[1]
			path := fmt.Sprintf("/%d/%d/%d.png", area.Zoom, x, y)
			want := parts[(x+y)%len(parts)] + ".tiles.test"
			hosts := log.paths[path]
			if len(hosts) != 1 || hosts[0] != want {
				t.Errorf("run %d: tile %d/%d/%d went to %v, want [%s]", run, area.Zoom, x, y, hosts, want)
			}
		}
	}
}

func TestEngineSendsTheTMSRowForAnInvertedMapSource(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "tms.xml", srv)

	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, area), filepath.Join(t.TempDir(), "out.sqlite"), nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	for pair := range area.IterTiles(t.Context()) {
		x, y := pair[0], pair[1]
		tms := (1<<area.Zoom - 1) - y
		if n := srv.Requests(testtiles.Tile{Z: area.Zoom, X: x, Y: tms}); n != 1 {
			t.Errorf("server got %d requests for row %d of XYZ tile %d/%d/%d, want 1", n, tms, area.Zoom, x, y)
		}
	}
	if n := srv.TotalRequests(); n != 9 {
		t.Errorf("server got %d tile requests, want 9", n)
	}
}
