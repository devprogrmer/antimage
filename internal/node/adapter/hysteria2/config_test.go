package hysteria2

import (
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func TestServiceParams_Validate(t *testing.T) {
	tests := []struct {
		name    string
		params  ServiceParams
		wantErr bool
	}{
		{
			name: "valid minimal params",
			params: ServiceParams{
				Port:     443,
				Password: "testpass123",
				CertFile: "/etc/hysteria2/cert.pem",
				KeyFile:  "/etc/hysteria2/key.pem",
			},
			wantErr: false,
		},
		{
			name: "invalid port too low",
			params: ServiceParams{
				Port:     0,
				Password: "testpass123",
				CertFile: "/etc/hysteria2/cert.pem",
				KeyFile:  "/etc/hysteria2/key.pem",
			},
			wantErr: true,
		},
		{
			name: "invalid port too high",
			params: ServiceParams{
				Port:     65536,
				Password: "testpass123",
				CertFile: "/etc/hysteria2/cert.pem",
				KeyFile:  "/etc/hysteria2/key.pem",
			},
			wantErr: true,
		},
		{
			name: "password too short",
			params: ServiceParams{
				Port:     443,
				Password: "short",
				CertFile: "/etc/hysteria2/cert.pem",
				KeyFile:  "/etc/hysteria2/key.pem",
			},
			wantErr: true,
		},
		{
			name: "missing cert file",
			params: ServiceParams{
				Port:     443,
				Password: "testpass123",
				KeyFile:  "/etc/hysteria2/key.pem",
			},
			wantErr: true,
		},
		{
			name: "salamander obfs without password",
			params: ServiceParams{
				Port:     443,
				Password: "testpass123",
				CertFile: "/etc/hysteria2/cert.pem",
				KeyFile:  "/etc/hysteria2/key.pem",
				Obfs:     "salamander",
			},
			wantErr: true,
		},
		{
			name: "valid with salamander",
			params: ServiceParams{
				Port:         443,
				Password:     "testpass123",
				CertFile:     "/etc/hysteria2/cert.pem",
				KeyFile:      "/etc/hysteria2/key.pem",
				Obfs:         "salamander",
				ObfsPassword: "obfspass123",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.params.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGenerateConfig(t *testing.T) {
	params := ServiceParams{
		Port:     443,
		Password: "testpass123",
		CertFile: "/etc/hysteria2/cert.pem",
		KeyFile:  "/etc/hysteria2/key.pem",
		SNI:      "example.com",
		UpMbps:   100,
		DownMbps: 200,
	}

	users := []UserAuth{
		{Username: "user1", Password: "pass1"},
		{Username: "user2", Password: "pass2"},
	}

	config, err := GenerateConfig(123, params, users)
	if err != nil {
		t.Fatalf("GenerateConfig failed: %v", err)
	}

	// Verify marker line exists
	if !strings.HasPrefix(config, markerPrefix) {
		t.Errorf("config missing marker prefix")
	}

	// Verify essential fields present
	required := []string{
		"listen:",
		"tls:",
		"cert:",
		"key:",
		"auth:",
		"userpass:",
	}

	for _, req := range required {
		if !strings.Contains(config, req) {
			t.Errorf("config missing required field: %q", req)
		}
	}

	// Verify users are included
	if !strings.Contains(config, "user1") || !strings.Contains(config, "user2") {
		t.Errorf("config missing user entries")
	}
}

func TestGenerateConfig_Deterministic(t *testing.T) {
	params := ServiceParams{
		Port:     443,
		Password: "testpass123",
		CertFile: "/etc/hysteria2/cert.pem",
		KeyFile:  "/etc/hysteria2/key.pem",
	}

	users := []UserAuth{
		{Username: "charlie", Password: "pass3"},
		{Username: "alice", Password: "pass1"},
		{Username: "bob", Password: "pass2"},
	}

	config1, err := GenerateConfig(1, params, users)
	if err != nil {
		t.Fatalf("GenerateConfig failed: %v", err)
	}

	config2, err := GenerateConfig(1, params, users)
	if err != nil {
		t.Fatalf("GenerateConfig failed: %v", err)
	}

	if config1 != config2 {
		t.Errorf("GenerateConfig not deterministic")
	}

	// Verify users are sorted alphabetically
	aliceIdx := strings.Index(config1, "alice")
	bobIdx := strings.Index(config1, "bob")
	charlieIdx := strings.Index(config1, "charlie")

	if aliceIdx == -1 || bobIdx == -1 || charlieIdx == -1 {
		t.Fatal("users not found in config")
	}

	if !(aliceIdx < bobIdx && bobIdx < charlieIdx) {
		t.Errorf("users not sorted: alice@%d, bob@%d, charlie@%d", aliceIdx, bobIdx, charlieIdx)
	}
}

func TestParseMarker(t *testing.T) {
	tests := []struct {
		name          string
		line          string
		wantServiceID int64
		wantChecksum  string
		wantOK        bool
	}{
		{
			name:          "valid marker",
			line:          "# antimage: service=123 checksum=abc123",
			wantServiceID: 123,
			wantChecksum:  "abc123",
			wantOK:        true,
		},
		{
			name:   "not a marker",
			line:   "# some other comment",
			wantOK: false,
		},
		{
			name:   "empty line",
			line:   "",
			wantOK: false,
		},
		{
			name:   "malformed marker",
			line:   "# antimage: invalid",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSvcID, gotChecksum, gotOK := parseMarker(tt.line)
			if gotOK != tt.wantOK {
				t.Errorf("parseMarker() gotOK = %v, want %v", gotOK, tt.wantOK)
			}
			if gotOK && (gotSvcID != tt.wantServiceID || gotChecksum != tt.wantChecksum) {
				t.Errorf("parseMarker() = (%v, %v), want (%v, %v)",
					gotSvcID, gotChecksum, tt.wantServiceID, tt.wantChecksum)
			}
		})
	}
}

func TestUserAuthFromSubjects(t *testing.T) {
	subjects := []adapter.Subject{
		{
			ID: 1,
			Credentials: []adapter.Credential{
				{Kind: string(adapter.CredPassword), Value: "pass1"},
			},
		},
		{
			ID: 2,
			Credentials: []adapter.Credential{
				{Kind: string(adapter.CredPassword), Value: "pass2"},
			},
		},
		{
			ID: 3,
			Credentials: []adapter.Credential{
				{Kind: string(adapter.CredUUID), Value: "not-a-password"},
			},
		},
	}

	users := UserAuthFromSubjects(subjects)

	// Should only extract subjects with password credentials
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}

	// Verify user mapping
	if users[0].Username != "user-1" || users[0].Password != "pass1" {
		t.Errorf("user 0 incorrect: %+v", users[0])
	}
	if users[1].Username != "user-2" || users[1].Password != "pass2" {
		t.Errorf("user 1 incorrect: %+v", users[1])
	}
}
