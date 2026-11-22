package tilecalc

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func TestIterTilesClosesTheChannelWhenTheContextEnds(t *testing.T) {
	tb := TileBounds{MinX: 0, MaxX: 99, MinY: 0, MaxY: 99, Zoom: 10}
	ctx, cancel := context.WithCancel(t.Context())
	tiles := tb.IterTiles(ctx)
	<-tiles
	cancel()

	// The producer closes the channel when it returns, so a closed channel shows that no goroutine leaks.
	timeout := time.After(time.Second)
	for received := 0; ; received++ {
		select {
		case _, open := <-tiles:
			if !open {
				return
			}
			// A select picks a ready case at random, so the producer can send one more tile after the cancel.
			if received > 0 {
				t.Fatalf("IterTiles sent %d tiles after the cancel, want at most 1", received+1)
			}
		case <-timeout:
			t.Fatal("IterTiles did not close its channel within 1 s of the cancel")
		}
	}
}

func TestIterTilesSendsEveryTileInRowOrder(t *testing.T) {
	tb := TileBounds{MinX: 3, MaxX: 4, MinY: 7, MaxY: 8, Zoom: 5}
	var got [][2]int
	for pair := range tb.IterTiles(t.Context()) {
		got = append(got, pair)
	}
	want := [][2]int{{3, 7}, {4, 7}, {3, 8}, {4, 8}}
	if len(got) != len(want) {
		t.Fatalf("IterTiles sent %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("IterTiles sent %v, want %v", got, want)
		}
	}
}

func TestGeographicBounds(t *testing.T) {
	_, err := NewGeographicBounds(41, 40, -74, -73)
	if err == nil {
		t.Error("expected error for min_lat >= max_lat")
	}
	_, err = NewGeographicBounds(40, 41, -73, -74)
	if err == nil {
		t.Error("expected error for min_lon >= max_lon")
	}
	_, err = NewGeographicBounds(40, 41, -74, -73)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGeographicBounds_Center(t *testing.T) {
	b, _ := NewGeographicBounds(40, 42, -74, -72)
	lat, lon := b.Center()
	if lat != 41 || lon != -73 {
		t.Errorf("expected (41, -73), got (%.2f, %.2f)", lat, lon)
	}
}

func TestTileBounds_TileCount(t *testing.T) {
	tb := &TileBounds{MinX: 10, MaxX: 15, MinY: 20, MaxY: 25, Zoom: 10}
	if tb.TileCount() != 36 {
		t.Errorf("expected 36, got %d", tb.TileCount())
	}
}

func TestTileCalculator_LatLonToTile(t *testing.T) {
	calc := NewTileCalculator()
	x, y := calc.LatLonToTile(0, 0, 1)
	if x != 1 || y != 1 {
		t.Errorf("center at zoom 1: expected (1,1), got (%d,%d)", x, y)
	}
	x, y = calc.LatLonToTile(40.7128, -74.0060, 10)
	if x != 301 || y != 385 {
		t.Errorf("NYC at zoom 10: expected (301,385), got (%d,%d)", x, y)
	}
}

func TestTileCalculator_TileToLatLon(t *testing.T) {
	calc := NewTileCalculator()
	lat, lon := calc.TileToLatLon(1, 1, 1)
	if math.Abs(lat) > 0.2 || math.Abs(lon) > 0.2 {
		t.Errorf("expected near (0,0), got (%.2f, %.2f)", lat, lon)
	}
}

func TestTileCalculator_EstimateDownloadSize(t *testing.T) {
	calc := NewTileCalculator()
	tb := &TileBounds{MinX: 0, MaxX: 9, MinY: 0, MaxY: 9, Zoom: 5}
	est := calc.EstimateDownloadSize(tb, 20)
	if est["tile_count"].(int) != 100 {
		t.Errorf("expected 100 tiles, got %d", est["tile_count"])
	}
	if est["size_kb"].(float64) != 2000 {
		t.Errorf("expected 2000 KB, got %f", est["size_kb"])
	}
}

func TestTileCalculator_ValidateZoomRange(t *testing.T) {
	calc := NewTileCalculator()
	if len(calc.ValidateZoomRange(5, 15)) != 0 {
		t.Error("expected no errors for valid range")
	}
	if len(calc.ValidateZoomRange(-1, 15)) == 0 {
		t.Error("expected error for negative min zoom")
	}
	if len(calc.ValidateZoomRange(15, 5)) == 0 {
		t.Error("expected error for min > max")
	}
}

func TestTileSizeKBFollowsTheTileType(t *testing.T) {
	tests := []struct {
		tileType string
		want     float64
	}{
		{"jpg", 18},
		{"JPG", 18},
		{"jpeg", 18},
		{"png", 25},
		{"PNG", 25},
		{"webp", 20},
		{"", 20},
	}
	for _, tt := range tests {
		if got := TileSizeKB(tt.tileType); got != tt.want {
			t.Errorf("TileSizeKB(%q) = %v, want %v", tt.tileType, got, tt.want)
		}
	}
}

func TestNewGeographicBoundsRejectsBoundsWhollyOutsideWebMercator(t *testing.T) {
	tests := []struct {
		name           string
		minLat, maxLat float64
		wantErr        bool
	}{
		{"accepts bounds that start at the north limit", 85.0511, 86, false},
		{"rejects bounds that start just above the north limit", 85.0512, 86, true},
		{"accepts bounds that end at the south limit", -86, -85.0511, false},
		{"rejects bounds that end just below the south limit", -86, -85.0512, true},
		{"accepts bounds that cross the north limit", 80, 89, false},
		{"rejects bounds from 86 to 89", 86, 89, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewGeographicBounds(tt.minLat, tt.maxLat, 0, 1)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewGeographicBounds(%g, %g) error = %v, want an error: %t", tt.minLat, tt.maxLat, err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "outside the Web Mercator range -85.0511 to 85.0511") {
				t.Errorf("error = %q, want the Web Mercator range", err)
			}
		})
	}
}

func TestTileBoundsToGeographicAcceptsTheTopTile(t *testing.T) {
	// The north edge of tile row 0 is 85.05113 degrees, above MaxLatitude, and the tile is in Web Mercator.
	if gb := NewTileCalculator().TileBoundsToGeographic(&TileBounds{MinX: 0, MaxX: 0, MinY: 0, MaxY: 0, Zoom: 3}); gb == nil {
		t.Fatal("TileBoundsToGeographic of tile row 0 = nil, want the tile bounds")
	}
}
