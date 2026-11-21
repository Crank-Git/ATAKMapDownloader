package commands

import "testing"

func TestDownloadConfigVerifyTLS(t *testing.T) {
	tests := []struct {
		name string
		adv  map[string]interface{}
		want bool
	}{
		{"verifies TLS when the settings omit both keys", map[string]interface{}{}, true},
		{"turns off verification when verify_tls is false", map[string]interface{}{"verify_tls": false}, false},
		{"turns off verification when the old verify_ssl key is false", map[string]interface{}{"verify_ssl": false}, false},
		{"uses verify_tls when the settings hold both keys", map[string]interface{}{"verify_tls": true, "verify_ssl": false}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := downloadConfig(tt.adv).VerifyTLS; got != tt.want {
				t.Errorf("downloadConfig(%v).VerifyTLS = %v, want %v", tt.adv, got, tt.want)
			}
		})
	}
}
