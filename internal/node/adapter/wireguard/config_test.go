package wireguard

import (
	"strings"
	"testing"
)

func TestServiceParams_Validate(t *testing.T) {
	tests := []struct {
		name    string
		params  ServiceParams
		wantErr bool
	}{
		{
			name: "valid params",
			params: ServiceParams{
				Port:       51820,
				Subnet:     "10.8.0.1/24",
				PrivateKey: "YGBDUkkPpnLsm8pGDxmHWsxAH8KN4JWZ8H5L9T5HhV4=",
			},
			wantErr: false,
		},
		{
			name: "invalid port too low",
			params: ServiceParams{
				Port:       0,
				Subnet:     "10.8.0.1/24",
				PrivateKey: "YGBDUkkPpnLsm8pGDxmHWsxAH8KN4JWZ8H5L9T5HhV4=",
			},
			wantErr: true,
		},
		{
			name: "invalid port too high",
			params: ServiceParams{
				Port:       65536,
				Subnet:     "10.8.0.1/24",
				PrivateKey: "YGBDUkkPpnLsm8pGDxmHWsxAH8KN4JWZ8H5L9T5HhV4=",
			},
			wantErr: true,
		},
		{
			name: "invalid subnet",
			params: ServiceParams{
				Port:       51820,
				Subnet:     "not-a-subnet",
				PrivateKey: "YGBDUkkPpnLsm8pGDxmHWsxAH8KN4JWZ8H5L9T5HhV4=",
			},
			wantErr: true,
		},
		{
			name: "invalid private key length",
			params: ServiceParams{
				Port:       51820,
				Subnet:     "10.8.0.1/24",
				PrivateKey: "short",
			},
			wantErr: true,
		},
		{
			name: "invalid MTU too low",
			params: ServiceParams{
				Port:       51820,
				Subnet:     "10.8.0.1/24",
				PrivateKey: "YGBDUkkPpnLsm8pGDxmHWsxAH8KN4JWZ8H5L9T5HhV4=",
				MTU:        1000,
			},
			wantErr: true,
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
		Port:       51820,
		Subnet:     "10.8.0.1/24",
		PrivateKey: "YGBDUkkPpnLsm8pGDxmHWsxAH8KN4JWZ8H5L9T5HhV4=",
		DNS:        []string{"1.1.1.1", "8.8.8.8"},
		MTU:        1420,
	}

	peers := []PeerConfig{
		{
			PublicKey:  "peer1pubkey==",
			AllowedIPs: "10.8.0.2/32",
			Keepalive:  25,
		},
		{
			PublicKey:  "peer2pubkey==",
			AllowedIPs: "10.8.0.3/32",
			Keepalive:  25,
		},
	}

	config, err := GenerateConfig(123, params, peers)
	if err != nil {
		t.Fatalf("GenerateConfig failed: %v", err)
	}

	// Verify marker line exists
	if !strings.HasPrefix(config, markerPrefix) {
		t.Errorf("config missing marker prefix")
	}

	// Verify essential sections present
	requiredSections := []string{
		"[Interface]",
		"PrivateKey = " + params.PrivateKey,
		"ListenPort = 51820",
		"Address = 10.8.0.1/24",
		"MTU = 1420",
		"DNS = 1.1.1.1, 8.8.8.8",
		"[Peer]",
		"PublicKey = peer1pubkey==",
		"AllowedIPs = 10.8.0.2/32",
		"PersistentKeepalive = 25",
		"PublicKey = peer2pubkey==",
	}

	for _, required := range requiredSections {
		if !strings.Contains(config, required) {
			t.Errorf("config missing required section: %q", required)
		}
	}
}

func TestGenerateConfig_Deterministic(t *testing.T) {
	params := ServiceParams{
		Port:       51820,
		Subnet:     "10.8.0.1/24",
		PrivateKey: "YGBDUkkPpnLsm8pGDxmHWsxAH8KN4JWZ8H5L9T5HhV4=",
	}

	peers := []PeerConfig{
		{PublicKey: "bbb==", AllowedIPs: "10.8.0.3/32", Keepalive: 25},
		{PublicKey: "aaa==", AllowedIPs: "10.8.0.2/32", Keepalive: 25},
		{PublicKey: "ccc==", AllowedIPs: "10.8.0.4/32", Keepalive: 25},
	}

	config1, err := GenerateConfig(1, params, peers)
	if err != nil {
		t.Fatalf("GenerateConfig failed: %v", err)
	}

	// Generate again with same inputs
	config2, err := GenerateConfig(1, params, peers)
	if err != nil {
		t.Fatalf("GenerateConfig failed: %v", err)
	}

	if config1 != config2 {
		t.Errorf("GenerateConfig not deterministic:\n%s\n\nvs\n\n%s", config1, config2)
	}

	// Verify peers are sorted (aaa, bbb, ccc)
	aaaIdx := strings.Index(config1, "PublicKey = aaa==")
	bbbIdx := strings.Index(config1, "PublicKey = bbb==")
	cccIdx := strings.Index(config1, "PublicKey = ccc==")

	if aaaIdx == -1 || bbbIdx == -1 || cccIdx == -1 {
		t.Fatal("peers not found in config")
	}

	if !(aaaIdx < bbbIdx && bbbIdx < cccIdx) {
		t.Errorf("peers not sorted: aaa@%d, bbb@%d, ccc@%d", aaaIdx, bbbIdx, cccIdx)
	}
}

func TestAllocatePeerIP(t *testing.T) {
	tests := []struct {
		name      string
		subnet    string
		subjectID int64
		want      string
		wantErr   bool
	}{
		{
			name:      "first peer",
			subnet:    "10.8.0.1/24",
			subjectID: 1,
			want:      "10.8.0.2/32",
			wantErr:   false,
		},
		{
			name:      "second peer",
			subnet:    "10.8.0.1/24",
			subjectID: 2,
			want:      "10.8.0.3/32",
			wantErr:   false,
		},
		{
			name:      "high subject ID",
			subnet:    "10.8.0.1/24",
			subjectID: 100,
			want:      "10.8.0.101/32",
			wantErr:   false,
		},
		{
			name:      "wraps within subnet",
			subnet:    "10.8.0.1/24",
			subjectID: 300,
			want:      "10.8.0.47/32", // 300 % 253 + 1 = 47
			wantErr:   false,
		},
		{
			name:      "invalid subnet",
			subnet:    "invalid",
			subjectID: 1,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AllocatePeerIP(tt.subnet, tt.subjectID)
			if (err != nil) != tt.wantErr {
				t.Errorf("AllocatePeerIP() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("AllocatePeerIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseMarker(t *testing.T) {
	tests := []struct {
		name            string
		line            string
		wantServiceID   int64
		wantChecksum    string
		wantOK          bool
	}{
		{
			name:            "valid marker",
			line:            "# antimage: service=123 checksum=abcdef123456",
			wantServiceID:   123,
			wantChecksum:    "abcdef123456",
			wantOK:          true,
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
			line:   "# antimage: something wrong",
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
