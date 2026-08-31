package l2tp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// A node with no L2TP/IPsec service configured has nothing to restart. This
// is the one branch of Restart that is fully testable without root: it
// returns before ever shelling out to systemctl.
func TestRestart_UnconfiguredReturnsUnsupported(t *testing.T) {
	a := New(t.TempDir(), t.TempDir())
	err := a.Restart(context.Background())
	if !errors.Is(err, adapter.ErrRestartUnsupported) {
		t.Errorf("Restart on an unconfigured node = %v, want ErrRestartUnsupported", err)
	}
}

// Writing the marker file Apply's own install step relies on must be enough
// to make Restart consider the service configured -- it uses the same check,
// deliberately, so the two cannot silently disagree about what "configured"
// means.
func TestRestart_RecognizesTheSameMarkerApplyWrites(t *testing.T) {
	confDir := t.TempDir()
	a := New(confDir, t.TempDir())

	if err := os.MkdirAll(filepath.Join(confDir, "xl2tpd"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "xl2tpd", "xl2tpd.conf"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Past this point Restart calls the real systemctl, which needs root
	// and a running systemd -- not available in this unit test environment.
	// The CI realruntime job (which runs as root under systemd) is where the
	// actual restart call is proven; see TestObservePlanApplyCycle in
	// integration_test.go for the same root-gated shape.
	if os.Getuid() != 0 {
		t.Skip("configured-restart path calls systemctl; requires root, see realruntime CI job")
	}
	if err := a.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
}
