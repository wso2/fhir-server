package config

import "testing"

// TestExampleConfigParses guards config.example.yaml against drifting from the
// FileConfig schema (unknown keys are rejected at load time).
func TestExampleConfigParses(t *testing.T) {
	if _, err := LoadFromPath("../../config.example.yaml"); err != nil {
		t.Fatalf("config.example.yaml no longer parses: %v", err)
	}
}
