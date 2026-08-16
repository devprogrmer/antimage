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

// Property: JCS sorts object keys by UTF-16 code unit, not by UTF-8 byte.
// Go's encoding/json sorts map keys by UTF-8 byte order, which agrees with
// UTF-16 ordering for the Basic Multilingual Plane but disagrees for
// supplementary-plane characters (surrogate pairs). This test would pass
// against a naive `return json.Marshal(v)` implementation for every other
// test in this file, but fails against it here — it is the one case that
// actually requires jcs.Transform's UTF-16 sort rather than encoding/json's
// byte sort.
//
// "\U0001F600" (an emoji) is UTF-16 D83D DE00 but UTF-8 F0 9F 98 80.
// "￿" is UTF-16 FFFF but UTF-8 EF BF BF.
// Under UTF-16 code-unit order, D83D < FFFF, so the emoji key sorts first.
// Under UTF-8 byte order, F0 > EF, so the emoji key sorts last. The two
// orderings disagree, which is exactly what this test pins.
func TestSupplementaryPlaneKeysUseUTF16Ordering(t *testing.T) {
	m := map[string]any{
		"\U0001F600": "emoji",
		"￿":     "bmp-max",
	}
	got, err := Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"😀":"emoji","￿":"bmp-max"}`
	if string(got) != want {
		t.Fatalf("canonical form = %s, want %s (UTF-16 code-unit order: emoji key D83D < FFFF key, so emoji must sort first)", got, want)
	}
}

// RFC 8785 canonicalizes every number as an IEEE-754 double. This pins the
// documented precision boundary: integers up to 2^53 round-trip exactly,
// but 2^53+1 is not representable as a double and silently rounds to the
// nearest representable value during canonicalization. This is correct
// RFC 8785 behavior, not a bug — the test exists so that if the underlying
// library's number formatting ever changes, this fails loudly instead of
// producing a silent hash mismatch somewhere else in the system.
func TestLargeIntegerPrecisionBoundary(t *testing.T) {
	type doc struct {
		N int64 `json:"n"`
	}

	const maxSafeInt = int64(1) << 53 // 9007199254740992

	safe, err := Marshal(doc{N: maxSafeInt})
	if err != nil {
		t.Fatalf("Marshal(2^53): %v", err)
	}
	wantSafe := `{"n":9007199254740992}`
	if string(safe) != wantSafe {
		t.Fatalf("Marshal(2^53) = %s, want %s — 2^53 must round-trip exactly", safe, wantSafe)
	}

	unsafe, err := Marshal(doc{N: maxSafeInt + 1})
	if err != nil {
		t.Fatalf("Marshal(2^53+1): %v", err)
	}
	// 2^53+1 (9007199254740993) is not representable as a float64; it rounds
	// to the nearest even representable double, which is 2^53 itself. This
	// is the observed, pinned behavior of jcs.Transform's number formatting.
	wantUnsafe := `{"n":9007199254740992}`
	if string(unsafe) != wantUnsafe {
		t.Fatalf("Marshal(2^53+1) = %s, want %s — silent precision loss changed shape; update this pin only after confirming the new behavior is intentional", unsafe, wantUnsafe)
	}
}
