package xray

import (
	"encoding/json"
	"testing"
)

func TestRealityValidation(t *testing.T) {
	tests := []struct {
		name    string
		inbound Inbound
		wantErr bool
	}{
		{
			name: "valid reality config",
			inbound: Inbound{
				Protocol:    VLESS,
				Port:        443,
				Listen:      "0.0.0.0",
				Network:     TCP,
				Security:    SecurityReality,
				Dest:        "www.microsoft.com:443",
				ServerNames: []string{"www.microsoft.com", "microsoft.com"},
				PrivateKey:  "gJWXYz_VwXmLh5Eo5T8sRWN9-KNmFN1VjnLXHb9aU3g",
				ShortIDs:    []string{"6ba85179e30d4fc2", ""},
			},
			wantErr: false,
		},
		{
			name: "reality missing dest",
			inbound: Inbound{
				Protocol:    VLESS,
				Port:        443,
				Security:    SecurityReality,
				ServerNames: []string{"example.com"},
				PrivateKey:  "testkey",
				ShortIDs:    []string{"testid"},
			},
			wantErr: true,
		},
		{
			name: "reality missing server_names",
			inbound: Inbound{
				Protocol:   VLESS,
				Port:       443,
				Security:   SecurityReality,
				Dest:       "example.com:443",
				PrivateKey: "testkey",
				ShortIDs:   []string{"testid"},
			},
			wantErr: true,
		},
		{
			name: "reality missing private_key",
			inbound: Inbound{
				Protocol:    VLESS,
				Port:        443,
				Security:    SecurityReality,
				Dest:        "example.com:443",
				ServerNames: []string{"example.com"},
				ShortIDs:    []string{"testid"},
			},
			wantErr: true,
		},
		{
			name: "reality missing short_ids",
			inbound: Inbound{
				Protocol:    VLESS,
				Port:        443,
				Security:    SecurityReality,
				Dest:        "example.com:443",
				ServerNames: []string{"example.com"},
				PrivateKey:  "testkey",
			},
			wantErr: true,
		},
		{
			name: "reality only works with vless",
			inbound: Inbound{
				Protocol:    VLESS,
				Port:        443,
				Security:    SecurityReality,
				Dest:        "example.com:443",
				ServerNames: []string{"example.com"},
				PrivateKey:  "testkey",
				ShortIDs:    []string{"testid"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.inbound.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRealityGeneration(t *testing.T) {
	inbound := Inbound{
		Protocol:    VLESS,
		Port:        443,
		Listen:      "0.0.0.0",
		Network:     TCP,
		Security:    SecurityReality,
		Dest:        "www.microsoft.com:443",
		ServerNames: []string{"www.microsoft.com", "microsoft.com"},
		PrivateKey:  "gJWXYz_VwXmLh5Eo5T8sRWN9-KNmFN1VjnLXHb9aU3g",
		ShortIDs:    []string{"6ba85179e30d4fc2", ""},
		Sniffing:    true,
	}

	users := []User{
		{
			SubjectID:  1,
			Email:      "user1@example.com",
			Credential: "b1e8f2c0-3d4e-4a5b-9c6d-7e8f9a0b1c2d",
		},
		{
			SubjectID:  2,
			Email:      "user2@example.com",
			Credential: "c2f9a3d1-4e5f-5b6c-0d7e-8f9a0b1c2d3e",
		},
	}

	config, err := inbound.Generate(users)
	if err != nil {
		t.Fatalf("Generate() failed: %v", err)
	}

	// Verify it's valid JSON
	var doc map[string]any
	if err := json.Unmarshal(config, &doc); err != nil {
		t.Fatalf("generated config is not valid JSON: %v", err)
	}

	// Verify structure
	inbounds, ok := doc["inbounds"].([]any)
	if !ok || len(inbounds) == 0 {
		t.Fatal("expected inbounds array in generated config")
	}

	inb := inbounds[0].(map[string]any)

	// Verify stream settings contain realitySettings
	stream, ok := inb["streamSettings"].(map[string]any)
	if !ok {
		t.Fatal("expected streamSettings in inbound")
	}

	reality, ok := stream["realitySettings"].(map[string]any)
	if !ok {
		t.Fatal("expected realitySettings in streamSettings")
	}

	// Verify Reality fields
	if reality["dest"] != "www.microsoft.com:443" {
		t.Errorf("expected dest www.microsoft.com:443, got %v", reality["dest"])
	}

	serverNames, ok := reality["serverNames"].([]any)
	if !ok || len(serverNames) != 2 {
		t.Errorf("expected 2 server names, got %v", reality["serverNames"])
	}

	if reality["privateKey"] != "gJWXYz_VwXmLh5Eo5T8sRWN9-KNmFN1VjnLXHb9aU3g" {
		t.Errorf("expected privateKey to match, got %v", reality["privateKey"])
	}

	shortIds, ok := reality["shortIds"].([]any)
	if !ok || len(shortIds) != 2 {
		t.Errorf("expected 2 short IDs, got %v", reality["shortIds"])
	}

	// Verify VLESS clients have flow set
	settings, ok := inb["settings"].(map[string]any)
	if !ok {
		t.Fatal("expected settings in inbound")
	}

	clients, ok := settings["clients"].([]any)
	if !ok || len(clients) != 2 {
		t.Fatalf("expected 2 clients, got %v", settings["clients"])
	}

	client1 := clients[0].(map[string]any)
	if client1["flow"] != "xtls-rprx-vision" {
		t.Errorf("expected VLESS Reality client to have flow xtls-rprx-vision, got %v", client1["flow"])
	}
}

func TestRealityParsing(t *testing.T) {
	raw := json.RawMessage(`{
		"protocol": "vless",
		"port": 443,
		"listen": "0.0.0.0",
		"network": "tcp",
		"security": "reality",
		"dest": "www.microsoft.com:443",
		"server_names": ["www.microsoft.com", "microsoft.com"],
		"private_key": "gJWXYz_VwXmLh5Eo5T8sRWN9-KNmFN1VjnLXHb9aU3g",
		"short_ids": ["6ba85179e30d4fc2", ""],
		"sniffing": true
	}`)

	inbound, err := ParseInbound(raw)
	if err != nil {
		t.Fatalf("ParseInbound() failed: %v", err)
	}

	if inbound.Security != SecurityReality {
		t.Errorf("expected security=reality, got %s", inbound.Security)
	}

	if inbound.Dest != "www.microsoft.com:443" {
		t.Errorf("expected dest=www.microsoft.com:443, got %s", inbound.Dest)
	}

	if len(inbound.ServerNames) != 2 {
		t.Errorf("expected 2 server names, got %d", len(inbound.ServerNames))
	}

	if inbound.PrivateKey != "gJWXYz_VwXmLh5Eo5T8sRWN9-KNmFN1VjnLXHb9aU3g" {
		t.Errorf("expected private_key to match, got %s", inbound.PrivateKey)
	}

	if len(inbound.ShortIDs) != 2 {
		t.Errorf("expected 2 short IDs, got %d", len(inbound.ShortIDs))
	}
}
