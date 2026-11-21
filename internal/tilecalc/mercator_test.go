package tilecalc

import (
	"math"
	"testing"
)

func TestMercatorBoundsReturnsTheTileEdgesInMetres(t *testing.T) {
	// half is the distance from the origin of EPSG:3857 to the edge of the world.
	const half = 20037508.342789244
	tests := []struct {
		name                   string
		x, y, z                int
		minX, minY, maxX, maxY float64
	}{
		{"covers the world at zoom level 0", 0, 0, 0, -half, -half, half, half},
		{"covers the north-east quarter for tile 1/1/0", 1, 0, 1, 0, 0, half, half},
		{"covers the south-west quarter for tile 1/0/1", 0, 1, 1, -half, -half, 0, 0},
		{"covers one sixteenth for tile 2/3/3", 3, 3, 2, half / 2, -half, half, -half / 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minX, minY, maxX, maxY := MercatorBounds(tt.x, tt.y, tt.z)
			got := []float64{minX, minY, maxX, maxY}
			want := []float64{tt.minX, tt.minY, tt.maxX, tt.maxY}
			for i := range got {
				if math.Abs(got[i]-want[i]) > 1e-6 {
					t.Fatalf("MercatorBounds(%d, %d, %d) = %v, want %v", tt.x, tt.y, tt.z, got, want)
				}
			}
		})
	}
}
