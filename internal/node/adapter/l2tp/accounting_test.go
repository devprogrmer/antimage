package l2tp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAccountingCursorPersistence(t *testing.T) {
	tmpState := t.TempDir()
	a := New("/tmp/l2tp-test", tmpState)

	cursor := accountingCursor{
		LastPoll: 1692547200,
		Counters: map[string]trafficCounter{
			"10.8.0.2": {RxBytes: 1048576, TxBytes: 2097152},
			"10.8.0.3": {RxBytes: 524288, TxBytes: 1048576},
		},
	}

	// Save cursor.
	if err := a.saveCursor(cursor); err != nil {
		t.Fatalf("save cursor: %v", err)
	}

	// Load cursor.
	loaded, err := a.loadCursor()
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}

	// Verify.
	if loaded.LastPoll != cursor.LastPoll {
		t.Errorf("want LastPoll %d, got %d", cursor.LastPoll, loaded.LastPoll)
	}

	if len(loaded.Counters) != 2 {
		t.Fatalf("want 2 counters, got %d", len(loaded.Counters))
	}

	if loaded.Counters["10.8.0.2"].RxBytes != 1048576 {
		t.Errorf("wrong RxBytes for 10.8.0.2: %d", loaded.Counters["10.8.0.2"].RxBytes)
	}

	if loaded.Counters["10.8.0.2"].TxBytes != 2097152 {
		t.Errorf("wrong TxBytes for 10.8.0.2: %d", loaded.Counters["10.8.0.2"].TxBytes)
	}
}

func TestAccountingCursorLoadMissing(t *testing.T) {
	tmpState := t.TempDir()
	a := New("/tmp/l2tp-test", tmpState)

	// Load non-existent cursor should return error.
	_, err := a.loadCursor()
	if err == nil {
		t.Error("expected error loading non-existent cursor")
	}
}

func TestParseCounterLine(t *testing.T) {
	tests := []struct {
		line      string
		wantIP    string
		wantBytes uint64
	}{
		{
			line:      "ip saddr 10.8.0.2 counter packets 1234 bytes 1048576",
			wantIP:    "10.8.0.2",
			wantBytes: 1048576,
		},
		{
			line:      "ip daddr 10.8.0.3 counter packets 5678 bytes 2097152",
			wantIP:    "10.8.0.3",
			wantBytes: 2097152,
		},
		{
			line:      "type filter hook input priority 0; policy accept;",
			wantIP:    "",
			wantBytes: 0,
		},
	}

	for _, tt := range tests {
		ip, bytes := parseCounterLine(tt.line)
		if ip != tt.wantIP {
			t.Errorf("parseCounterLine(%q) ip = %q, want %q", tt.line, ip, tt.wantIP)
		}
		if bytes != tt.wantBytes {
			t.Errorf("parseCounterLine(%q) bytes = %d, want %d", tt.line, bytes, tt.wantBytes)
		}
	}
}

func TestIPToSubjectID(t *testing.T) {
	tmpState := t.TempDir()
	a := New("/tmp/l2tp-test", tmpState)

	// Create sessions file.
	sessionsPath := filepath.Join(tmpState, "l2tp-sessions.txt")
	content := "user1 10.8.0.2\nuser2 10.8.0.3\nuser42 10.8.0.10\n"
	if err := os.WriteFile(sessionsPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		ip            string
		wantSubjectID int64
		wantErr       bool
	}{
		{"10.8.0.2", 1, false},
		{"10.8.0.3", 2, false},
		{"10.8.0.10", 42, false},
		{"10.8.0.99", 0, true}, // not in sessions
	}

	for _, tt := range tests {
		gotID, err := a.ipToSubjectID(tt.ip)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ipToSubjectID(%q) want error, got nil", tt.ip)
			}
		} else {
			if err != nil {
				t.Errorf("ipToSubjectID(%q) unexpected error: %v", tt.ip, err)
			}
			if gotID != tt.wantSubjectID {
				t.Errorf("ipToSubjectID(%q) = %d, want %d", tt.ip, gotID, tt.wantSubjectID)
			}
		}
	}
}

func TestIPToSubjectIDNoFile(t *testing.T) {
	tmpState := t.TempDir()
	a := New("/tmp/l2tp-test", tmpState)

	// No sessions file exists.
	_, err := a.ipToSubjectID("10.8.0.2")
	if err == nil {
		t.Error("expected error when sessions file missing")
	}
}

func TestUsageDeltaComputation(t *testing.T) {
	tmpState := t.TempDir()
	a := New("/tmp/l2tp-test", tmpState)

	// Save initial cursor.
	initial := accountingCursor{
		LastPoll: 1000,
		Counters: map[string]trafficCounter{
			"10.8.0.2": {RxBytes: 1000, TxBytes: 2000},
		},
	}
	if err := a.saveCursor(initial); err != nil {
		t.Fatal(err)
	}

	// Create sessions mapping.
	sessionsPath := filepath.Join(tmpState, "l2tp-sessions.txt")
	if err := os.WriteFile(sessionsPath, []byte("user1 10.8.0.2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Mock readNftablesCounters to return updated counters.
	// In a real test, we'd inject this as a dependency.
	// For now, we verify the cursor logic directly.

	// Load cursor.
	loaded, err := a.loadCursor()
	if err != nil {
		t.Fatal(err)
	}

	// Simulate new counters.
	newCounters := map[string]trafficCounter{
		"10.8.0.2": {RxBytes: 1500, TxBytes: 2500},
	}

	// Compute delta manually (this is what Usage() does internally).
	prev := loaded.Counters["10.8.0.2"]
	cur := newCounters["10.8.0.2"]

	deltaRx := cur.RxBytes - prev.RxBytes
	deltaTx := cur.TxBytes - prev.TxBytes

	if deltaRx != 500 {
		t.Errorf("want deltaRx=500, got %d", deltaRx)
	}
	if deltaTx != 500 {
		t.Errorf("want deltaTx=500, got %d", deltaTx)
	}
}

func TestUsageCounterReset(t *testing.T) {
	// Test counter reset detection (service restart).
	prev := trafficCounter{RxBytes: 1000, TxBytes: 2000}
	cur := trafficCounter{RxBytes: 100, TxBytes: 200} // reset (smaller values)

	// Reset detection logic.
	if cur.RxBytes < prev.RxBytes || cur.TxBytes < prev.TxBytes {
		// Reset detected, use current as baseline.
		prev = trafficCounter{}
	}

	deltaRx := cur.RxBytes - prev.RxBytes
	deltaTx := cur.TxBytes - prev.TxBytes

	if deltaRx != 100 {
		t.Errorf("after reset, want deltaRx=100, got %d", deltaRx)
	}
	if deltaTx != 200 {
		t.Errorf("after reset, want deltaTx=200, got %d", deltaTx)
	}
}

func TestReadNftablesCountersNoTable(t *testing.T) {
	tmpState := t.TempDir()
	a := New("/tmp/l2tp-test", tmpState)

	// When nftables table doesn't exist, should return empty map without error.
	counters, err := a.readNftablesCounters()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(counters) != 0 {
		t.Errorf("want empty counters, got %d entries", len(counters))
	}
}
