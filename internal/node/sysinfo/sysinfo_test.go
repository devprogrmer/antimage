package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func procFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func TestSampleParsesProcFiles(t *testing.T) {
	root := procFixture(t, map[string]string{
		"loadavg": "0.75 0.42 0.31 2/431 12345\n",
		"uptime":  "864000.12 1700000.00\n",
		"meminfo": "MemTotal:        2048 kB\nSwapTotal:        512 kB\nMemAvailable:     512 kB\n",
	})

	m := sampleFrom(root)
	if m.Load1 != 0.75 {
		t.Errorf("Load1 = %v, want 0.75", m.Load1)
	}
	if m.UptimeS != 864000 {
		t.Errorf("UptimeS = %d, want 864000", m.UptimeS)
	}
	// (2048 - 512) kB used, reported in bytes.
	if want := uint64(1536 * 1024); m.MemUsed != want {
		t.Errorf("MemUsed = %d, want %d", m.MemUsed, want)
	}
}

// A heartbeat must never fail because a metric is unavailable: an agent that
// stops reporting in looks exactly like a dead node, which is a far worse
// outcome than a node reporting a load average of zero. On any host without
// /proc — macOS, Windows, a stripped container — Sample must degrade to zeros
// rather than panicking or blocking.
func TestSampleDegradesToZerosWithoutProc(t *testing.T) {
	m := sampleFrom(filepath.Join(t.TempDir(), "no-such-proc"))
	if m != (Metrics{}) {
		t.Errorf("Sample on a host with no /proc = %+v, want the zero Metrics", m)
	}
}

func TestSampleToleratesMalformedProcFiles(t *testing.T) {
	root := procFixture(t, map[string]string{
		"loadavg": "",
		"uptime":  "not-a-number\n",
		"meminfo": "MemTotal:\ngarbage\nMemAvailable: nonsense kB\n",
	})
	if m := sampleFrom(root); m != (Metrics{}) {
		t.Errorf("Sample on malformed /proc = %+v, want the zero Metrics", m)
	}
}

// MemAvailable above MemTotal (or a meminfo missing one of them) must not
// wrap the unsigned subtraction into a nonsense multi-exabyte reading.
func TestSampleNeverUnderflowsMemUsed(t *testing.T) {
	root := procFixture(t, map[string]string{
		"meminfo": "MemTotal:         512 kB\nMemAvailable:    2048 kB\n",
	})
	if m := sampleFrom(root); m.MemUsed != 0 {
		t.Errorf("MemUsed = %d, want 0 when MemAvailable exceeds MemTotal", m.MemUsed)
	}
}

// Sample is the exported entry point and must be callable on this host
// whatever it is, without error and without panic.
func TestSampleOnThisHostDoesNotFail(t *testing.T) {
	_ = Sample()
}
