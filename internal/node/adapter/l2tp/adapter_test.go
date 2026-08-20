package l2tp

import (
	"encoding/json"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func TestDescriptor(t *testing.T) {
	a := New("/tmp/l2tp-test-conf", "/tmp/l2tp-test-state")
	desc := a.Descriptor()

	if desc.Kind != Kind {
		t.Errorf("want kind %q, got %q", Kind, desc.Kind)
	}
	if desc.Version != "1" {
		t.Errorf("want version %q, got %q", "1", desc.Version)
	}
	if desc.Caps.HotUserAdd != true {
		t.Error("want HotUserAdd=true (CHAP reload + swanctl --load-creds)")
	}
	if desc.Caps.SelfAccounting != false {
		t.Error("want SelfAccounting=false (uses external nftables)")
	}
	if desc.Caps.RequiresPKI != false {
		t.Error("want RequiresPKI=false (uses PSK)")
	}
	if len(desc.Caps.CredentialKinds) != 1 || desc.Caps.CredentialKinds[0] != adapter.CredPassword {
		t.Errorf("want CredentialKinds=[password], got %v", desc.Caps.CredentialKinds)
	}

	// Verify schema is valid JSON.
	var schema map[string]interface{}
	if err := json.Unmarshal(desc.Caps.ServiceSchema, &schema); err != nil {
		t.Fatalf("invalid service schema: %v", err)
	}

	// Verify required fields are present in schema.
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema missing properties")
	}
	required := []string{"ip_range", "local_ip", "psk"}
	for _, field := range required {
		if _, exists := props[field]; !exists {
			t.Errorf("schema missing required field: %s", field)
		}
	}
}

func TestServiceSchemaStructure(t *testing.T) {
	a := New("/tmp/l2tp-test-conf", "/tmp/l2tp-test-state")
	desc := a.Descriptor()

	var schema map[string]interface{}
	if err := json.Unmarshal(desc.Caps.ServiceSchema, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	// Check schema type
	if schema["type"] != "object" {
		t.Errorf("want type=object, got %v", schema["type"])
	}

	// Check additionalProperties is false (strict schema)
	if schema["additionalProperties"] != false {
		t.Error("want additionalProperties=false")
	}

	// Check required array
	requiredRaw, ok := schema["required"].([]interface{})
	if !ok {
		t.Fatal("required field missing or wrong type")
	}
	required := make([]string, len(requiredRaw))
	for i, v := range requiredRaw {
		required[i] = v.(string)
	}
	expectedRequired := map[string]bool{"ip_range": true, "local_ip": true, "psk": true}
	for _, field := range required {
		if !expectedRequired[field] {
			t.Errorf("unexpected required field: %s", field)
		}
		delete(expectedRequired, field)
	}
	if len(expectedRequired) > 0 {
		t.Errorf("missing required fields: %v", expectedRequired)
	}
}

func TestAdapterKindConstant(t *testing.T) {
	if Kind != "l2tp" {
		t.Errorf("want Kind=%q, got %q", "l2tp", Kind)
	}
}
