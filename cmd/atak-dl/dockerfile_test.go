package main

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// dockerfileInstructions returns each instruction of the Dockerfile at the repository root, with no comment lines and no blank lines.
func dockerfileInstructions(t *testing.T) []string {
	t.Helper()
	// go test runs in the package directory, so the repository root is two levels up.
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	var instructions []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			instructions = append(instructions, line)
		}
	}
	return instructions
}

// TestDockerfileServesTheDataVolume checks the image of FR-team-server-30 and the nonroot user that the PM ruling for #60 keeps.
func TestDockerfileServesTheDataVolume(t *testing.T) {
	instructions := dockerfileInstructions(t)
	tests := []struct {
		name string
		want string
	}{
		{"runs serve on port 5000 with the data directory /data", `CMD ["serve", "--addr", "0.0.0.0:5000", "--data-dir", "/data"]`},
		{"declares /data as a volume", `VOLUME ["/data"]`},
		{"starts from the distroless nonroot image", "FROM gcr.io/distroless/static:nonroot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !slices.Contains(instructions, tt.want) {
				t.Errorf("Dockerfile has no instruction %q in %q", tt.want, instructions)
			}
		})
	}
	// A USER or RUN instruction could make the process root or chown the volume, and the image must do neither.
	for _, line := range instructions {
		if strings.HasPrefix(line, "USER") || strings.HasPrefix(line, "RUN") {
			t.Errorf("Dockerfile has the instruction %q, want the nonroot user of the base image and no RUN step", line)
		}
	}
}
