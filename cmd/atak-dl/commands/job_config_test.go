package commands

import "testing"

func TestJobRequestEngineConfigVerifiesTLS(t *testing.T) {
	off, on := false, true
	tests := []struct {
		name      string
		verifyTLS *bool
		want      bool
	}{
		{"verifies TLS when the request omits verify_tls", nil, true},
		{"turns off verification when verify_tls is false", &off, false},
		{"verifies TLS when verify_tls is true", &on, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (jobRequest{VerifyTLS: tt.verifyTLS}).engineConfig().VerifyTLS; got != tt.want {
				t.Errorf("engineConfig().VerifyTLS = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJobRequestEngineConfigTakesTheRateLimit(t *testing.T) {
	two := 2.0
	tests := []struct {
		name      string
		rateLimit *float64
		want      float64
	}{
		{"keeps the policy rate when the request omits rate_limit", nil, 0},
		{"takes rate_limit from the request", &two, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (jobRequest{RateLimit: tt.rateLimit}).engineConfig().RateLimit; got != tt.want {
				t.Errorf("engineConfig().RateLimit = %v, want %v", got, tt.want)
			}
		})
	}
}
