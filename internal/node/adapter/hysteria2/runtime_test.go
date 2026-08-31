package hysteria2

import (
	"path/filepath"
	"testing"
)

// The unit name is derived from the config path, so the two cannot drift.
// Getting this wrong means starting or stopping a DIFFERENT service than the
// one whose config was just written -- which is worse than failing, because it
// disrupts a service nobody asked to change while leaving the intended one
// stale.
func TestUnitIsDerivedFromTheConfigPath(t *testing.T) {
	r := NewExecRuntime()

	cases := []struct {
		path string
		want string
	}{
		{filepath.Join("/etc/hysteria2", "antimage-10.yaml"), "hysteria-server@antimage-10"},
		{filepath.Join("/etc/hysteria2", "antimage-7.yaml"), "hysteria-server@antimage-7"},
		{filepath.Join("/tmp/whatever", "antimage-42.yaml"), "hysteria-server@antimage-42"},
		// Bare filename, no directory.
		{"antimage-3.yaml", "hysteria-server@antimage-3"},
	}
	for _, tc := range cases {
		if got := r.unitFor(tc.path); got != tc.want {
			t.Errorf("unitFor(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// Two different services must never resolve to the same unit.
func TestDistinctServicesGetDistinctUnits(t *testing.T) {
	r := NewExecRuntime()
	a := &Adapter{dir: "/etc/hysteria2"}
	if u1, u2 := r.unitFor(a.configPath(1)), r.unitFor(a.configPath(2)); u1 == u2 {
		t.Errorf("services 1 and 2 share unit %q; stopping one would stop the other", u1)
	}
}

// The unit derived from a path the adapter itself produced must round-trip.
// This is the pairing that matters: Apply passes a.configPath(id), and the
// runtime has to recover the same id from it.
func TestUnitRoundTripsThroughConfigPath(t *testing.T) {
	r := NewExecRuntime()
	a := &Adapter{dir: t.TempDir()}
	if got, want := r.unitFor(a.configPath(99)), "hysteria-server@antimage-99"; got != want {
		t.Errorf("unitFor(configPath(99)) = %q, want %q", got, want)
	}
}
