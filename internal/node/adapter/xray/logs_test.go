package xray

import (
	"context"
	"errors"
	"testing"
)

func TestReadLogs_PassesThroughRuntimeOutput(t *testing.T) {
	rt := newFakeRuntime()
	rt.logOutput = "line one\nline two\n"
	a := New(t.TempDir(), rt, false)

	got, err := a.ReadLogs(context.Background(), 50)
	if err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	if got != rt.logOutput {
		t.Errorf("ReadLogs = %q, want %q", got, rt.logOutput)
	}
}

func TestReadLogs_PropagatesRuntimeError(t *testing.T) {
	rt := newFakeRuntime()
	rt.logErr = errors.New("journalctl: command not found")
	a := New(t.TempDir(), rt, false)

	if _, err := a.ReadLogs(context.Background(), 50); err == nil {
		t.Fatal("expected the runtime's error to propagate")
	}
}

// TestReadLogs_ClampsLineCount proves the adapter -- not just documents --
// enforces defaultLogLines/maxLogLines against whatever a caller (ultimately
// an HTTP query parameter, several layers removed) asks for.
func TestReadLogs_ClampsLineCount(t *testing.T) {
	cases := []struct {
		name      string
		requested int
		want      int
	}{
		{"zero uses the default", 0, defaultLogLines},
		{"negative uses the default", -5, defaultLogLines},
		{"in range is passed through unchanged", 500, 500},
		{"over the max is clamped", 1_000_000, maxLogLines},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := newFakeRuntime()
			a := New(t.TempDir(), rt, false)

			if _, err := a.ReadLogs(context.Background(), tc.requested); err != nil {
				t.Fatalf("ReadLogs: %v", err)
			}
			if rt.lastLinesAsked != tc.want {
				t.Errorf("runtime was asked for %d lines, want %d", rt.lastLinesAsked, tc.want)
			}
		})
	}
}
