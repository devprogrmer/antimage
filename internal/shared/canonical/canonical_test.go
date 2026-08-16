package canonical

import (
	"encoding/json"
	"math/rand"
	"testing"
)

// Property: insertion order into a map must never change the output.
// Go randomizes map iteration, but encoding/json sorts keys — this test
// guards against anyone "optimizing" that away with a custom encoder.
func TestMapInsertionOrderDoesNotAffectHash(t *testing.T) {
	keys := []string{"zeta", "alpha", "mu", "beta", "omega"}
	var want string
	for trial := 0; trial < 200; trial++ {
		m := map[string]any{}
		perm := rand.Perm(len(keys))
		for _, i := range perm {
			m[keys[i]] = i
		}
		_, got, err := Hash(m)
		if err != nil {
			t.Fatalf("Hash: %v", err)
		}
		if trial == 0 {
			want = got
			continue
		}
		if got != want {
			t.Fatalf("trial %d: hash %s != %s — canonicalization is order-dependent", trial, got, want)
		}
	}
}

// Property: struct field declaration order must not affect the output,
// because JCS sorts keys. Two structs with identical fields in different
// order must canonicalize identically.
func TestStructFieldOrderDoesNotAffectHash(t *testing.T) {
	type A struct {
		Zulu  string `json:"zulu"`
		Alpha string `json:"alpha"`
	}
	type B struct {
		Alpha string `json:"alpha"`
		Zulu  string `json:"zulu"`
	}
	_, ha, err := Hash(A{Zulu: "z", Alpha: "a"})
	if err != nil {
		t.Fatalf("Hash(A): %v", err)
	}
	_, hb, err := Hash(B{Alpha: "a", Zulu: "z"})
	if err != nil {
		t.Fatalf("Hash(B): %v", err)
	}
	if ha != hb {
		t.Fatalf("field order changed the hash: %s != %s", ha, hb)
	}
}

func TestKeysAreSortedAndWhitespaceStripped(t *testing.T) {
	got, _, err := Hash(map[string]any{"b": 1, "a": 2})
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if string(got) != `{"a":2,"b":1}` {
		t.Errorf("canonical form = %s, want {\"a\":2,\"b\":1}", got)
	}
}

// A nil slice and an empty slice must not collide, because the desired
// document distinguishes "no services" from "field absent".
func TestNilAndEmptySliceAreDistinguishable(t *testing.T) {
	type doc struct {
		Items []string `json:"items"`
	}
	nilBytes, _, err := Hash(doc{Items: nil})
	if err != nil {
		t.Fatalf("Hash(nil): %v", err)
	}
	emptyBytes, _, err := Hash(doc{Items: []string{}})
	if err != nil {
		t.Fatalf("Hash(empty): %v", err)
	}
	if string(nilBytes) != `{"items":null}` {
		t.Errorf("nil slice = %s, want {\"items\":null}", nilBytes)
	}
	if string(emptyBytes) != `{"items":[]}` {
		t.Errorf("empty slice = %s, want {\"items\":[]}", emptyBytes)
	}
}

func TestHashIsStableHex(t *testing.T) {
	_, h, err := Hash(json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(h))
	}
}
