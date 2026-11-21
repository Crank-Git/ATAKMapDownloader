package xmlparser

import (
	"path/filepath"
	"testing"
)

func TestParseFileReadsTheFixtureSourceFiles(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		wantURL string
		wantErr bool
	}{
		{"loads the XYZ fixture", "xyz.xml", "http://tiles.test/{$z}/{$x}/{$y}.png", false},
		{"loads the quadkey fixture", "quadkey.xml", "http://tiles.test/q/{$q}.png", false},
		{"rejects the customWmsMapSource fixture, which the parser does not load yet", "wms.xml", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, err := ParseFile(filepath.Join("..", "..", "testdata", "sources", tt.file))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseFile(%s) returned no error", tt.file)
				}
				return
			}
			if err != nil || source == nil {
				t.Fatalf("ParseFile(%s) = %v, %v", tt.file, source, err)
			}
			if source.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", source.URL, tt.wantURL)
			}
			if errs := New("").ValidateSource(source); len(errs) != 0 {
				t.Errorf("ValidateSource: %v", errs)
			}
		})
	}
}
