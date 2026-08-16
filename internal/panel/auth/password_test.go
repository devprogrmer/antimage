package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	ok, err := VerifyPassword(encoded, "correct horse battery staple")
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Error("correct password rejected")
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	encoded, _ := HashPassword("hunter2")
	ok, err := VerifyPassword(encoded, "hunter3")
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Error("wrong password accepted")
	}
}

func TestHashIsSaltedPerCall(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("identical passwords produced identical hashes — salt is missing or fixed")
	}
}

func TestEncodingIsPHCWithSpecParams(t *testing.T) {
	encoded, _ := HashPassword("x")
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("encoding = %q, want spec params m=65536,t=3,p=4", encoded)
	}
	if n := len(strings.Split(encoded, "$")); n != 6 {
		t.Errorf("PHC string has %d segments, want 6", n)
	}
}

func TestVerifyRejectsMalformedEncoding(t *testing.T) {
	for _, bad := range []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=65536,t=3,p=4$onlysalt",
		"$bcrypt$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
	} {
		if _, err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("VerifyPassword(%q) returned nil error", bad)
		}
	}
}
