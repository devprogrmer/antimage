package version

import "testing"

func TestDefaultsAreDevelopmentSafe(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty; ldflags may be absent in dev builds")
	}
	if Protocol < 1 {
		t.Fatalf("Protocol must be >= 1, got %d", Protocol)
	}
}
