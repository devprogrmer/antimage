package nodes

import (
	"encoding/json"
	"testing"

	"github.com/amyrm/antimage/internal/shared/secrets"
)

func TestSealAndOpenOutboundParams(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	params := json.RawMessage(`{"private_key":"secret","endpoint":"198.51.100.1:51820"}`)
	sealed, err := SealOutboundParams(box, params)
	if err != nil {
		t.Fatalf("SealOutboundParams: %v", err)
	}

	if sealed == string(params) {
		t.Errorf("SealOutboundParams returned plaintext, want sealed")
	}

	opened, err := OpenOutboundParams(box, sealed)
	if err != nil {
		t.Fatalf("OpenOutboundParams: %v", err)
	}

	if string(opened) != string(params) {
		t.Errorf("OpenOutboundParams = %s, want %s", opened, params)
	}
}

func TestOpenOutboundParamsBackwardCompatibility(t *testing.T) {
	key := make([]byte, 32)
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	// Plaintext JSON (unsealed) should be returned as-is
	plaintext := `{"port":443,"endpoint":"example.com"}`
	opened, err := OpenOutboundParams(box, plaintext)
	if err != nil {
		t.Fatalf("OpenOutboundParams(plaintext): %v", err)
	}

	if string(opened) != plaintext {
		t.Errorf("OpenOutboundParams(plaintext) = %s, want %s", opened, plaintext)
	}
}

func TestSealOutboundParamsEmptyInput(t *testing.T) {
	key := make([]byte, 32)
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}

	sealed, err := SealOutboundParams(box, nil)
	if err != nil {
		t.Fatalf("SealOutboundParams(nil): %v", err)
	}

	opened, err := OpenOutboundParams(box, sealed)
	if err != nil {
		t.Fatalf("OpenOutboundParams: %v", err)
	}

	if string(opened) != "{}" {
		t.Errorf("empty params round trip = %s, want {}", opened)
	}
}

func TestOpenOutboundParamsWrongKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	for i := range key2 {
		key2[i] = byte(255 - i)
	}

	box1, _ := secrets.NewBox(key1)
	box2, _ := secrets.NewBox(key2)

	params := json.RawMessage(`{"secret":"value"}`)
	sealed, err := SealOutboundParams(box1, params)
	if err != nil {
		t.Fatalf("SealOutboundParams: %v", err)
	}

	// Attempting to open with the wrong key should fail
	_, err = OpenOutboundParams(box2, sealed)
	if err == nil {
		t.Errorf("OpenOutboundParams with wrong key succeeded, want error")
	}
}
