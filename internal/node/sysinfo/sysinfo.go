// Package sysinfo reads coarse host metrics for heartbeats.
package sysinfo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcRoot is where Linux exposes the counters this package reads.
const ProcRoot = "/proc"

type Metrics struct {
	Load1   float64
	MemUsed uint64
	UptimeS uint64
}

// Sample reads /proc. Missing or unreadable files yield zeros rather than
// errors: a heartbeat must never fail because a metric is unavailable, and a
// node that stops reporting in is a far worse outcome than a node reporting a
// load average of zero. On a host without /proc — a developer's macOS or
// Windows machine — every field is simply zero.
func Sample() Metrics { return sampleFrom(ProcRoot) }

// sampleFrom is Sample with the procfs root injected, so the parsing and the
// degradation behaviour are testable on hosts that have no /proc.
func sampleFrom(root string) Metrics {
	var m Metrics

	if raw, err := os.ReadFile(filepath.Join(root, "loadavg")); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			m.Load1, _ = strconv.ParseFloat(fields[0], 64)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "uptime")); err == nil {
		if fields := strings.Fields(string(raw)); len(fields) > 0 {
			seconds, _ := strconv.ParseFloat(fields[0], 64)
			if seconds > 0 {
				m.UptimeS = uint64(seconds)
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(root, "meminfo")); err == nil {
		var total, available uint64
		for _, line := range strings.Split(string(raw), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			value, _ := strconv.ParseUint(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				total = value * 1024
			case "MemAvailable:":
				available = value * 1024
			}
		}
		if total > available {
			m.MemUsed = total - available
		}
	}
	return m
}
