package xray

import (
	"encoding/json"
	"fmt"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// PolicyConfig generates Xray policy configuration for user speed limits.
// This enables per-user bandwidth shaping at the protocol level.
type PolicyConfig struct {
	Levels map[string]LevelPolicy `json:"levels"`
	System SystemPolicy           `json:"system,omitempty"`
}

type LevelPolicy struct {
	StatsUserUplink   bool  `json:"statsUserUplink,omitempty"`
	StatsUserDownlink bool  `json:"statsUserDownlink,omitempty"`
	Handshake         *int  `json:"handshake,omitempty"`
	ConnIdle          *int  `json:"connIdle,omitempty"`
	UplinkOnly        *int  `json:"uplinkOnly,omitempty"`
	DownlinkOnly      *int  `json:"downlinkOnly,omitempty"`
	BufferSize        *int  `json:"bufferSize,omitempty"`
	// Speed limits in bytes/sec (Xray uses bytes, we receive kbps)
	UpSpeed   *int64 `json:"upSpeed,omitempty"`
	DownSpeed *int64 `json:"downSpeed,omitempty"`
}

type SystemPolicy struct {
	StatsInboundUplink    bool `json:"statsInboundUplink,omitempty"`
	StatsInboundDownlink  bool `json:"statsInboundDownlink,omitempty"`
	StatsOutboundUplink   bool `json:"statsOutboundUplink,omitempty"`
	StatsOutboundDownlink bool `json:"statsOutboundDownlink,omitempty"`
}

// GeneratePolicyConfig creates Xray policy configuration with per-user speed limits.
// This is written as a separate config file that Xray merges into its runtime config.
func GeneratePolicyConfig(subjects []adapter.Subject) ([]byte, error) {
	policy := PolicyConfig{
		Levels: make(map[string]LevelPolicy),
		System: SystemPolicy{
			StatsInboundUplink:    true,
			StatsInboundDownlink:  true,
			StatsOutboundUplink:   false,
			StatsOutboundDownlink: false,
		},
	}

	// Level "0" is the default for users without specific limits
	defaultLevel := LevelPolicy{
		StatsUserUplink:   true,
		StatsUserDownlink: true,
	}
	policy.Levels["0"] = defaultLevel

	// Create per-user levels with speed limits
	for _, subj := range subjects {
		if subj.SpeedLimitUpKbps == nil && subj.SpeedLimitDownKbps == nil {
			continue
		}

		level := LevelPolicy{
			StatsUserUplink:   true,
			StatsUserDownlink: true,
		}

		// Convert kbps to bytes/sec (kbps * 1024 / 8)
		if subj.SpeedLimitUpKbps != nil {
			bytesPerSec := (*subj.SpeedLimitUpKbps * 1024) / 8
			level.UpSpeed = &bytesPerSec
		}

		if subj.SpeedLimitDownKbps != nil {
			bytesPerSec := (*subj.SpeedLimitDownKbps * 1024) / 8
			level.DownSpeed = &bytesPerSec
		}

		// Use subject ID as level identifier
		levelKey := fmt.Sprintf("%d", subj.ID)
		policy.Levels[levelKey] = level
	}

	doc := map[string]any{"policy": policy}
	return json.MarshalIndent(doc, "", "  ")
}

// UserWithLevel extends User with policy level for speed limit enforcement.
type UserWithLevel struct {
	User
	Level int64 // Policy level (0 = default, subject ID = custom limits)
}

// GenerateWithPolicy renders the inbound with users assigned to policy levels.
func (in Inbound) GenerateWithPolicy(users []UserWithLevel) ([]byte, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	in = in.withDefaults()

	// Sort users by subject ID for determinism
	sorted := make([]UserWithLevel, len(users))
	copy(sorted, users)
	sortUsersByLevel(sorted)

	clients := make([]clientEntryWithLevel, 0, len(sorted))
	seen := make(map[string]struct{}, len(sorted))

	for _, u := range sorted {
		if u.Credential == "" {
			return nil, fmt.Errorf("%w: subject %d has no credential", ErrInvalidInbound, u.SubjectID)
		}

		if _, dup := seen[u.Email]; dup {
			return nil, fmt.Errorf("%w: duplicate user tag %q", ErrInvalidInbound, u.Email)
		}
		seen[u.Email] = struct{}{}

		entry := clientEntryWithLevel{
			Email: u.Email,
			Level: u.Level,
		}

		if in.Protocol == Trojan {
			entry.Password = u.Credential
		} else {
			entry.ID = u.Credential
		}

		// XTLS flow
		if in.Protocol == VLESS && in.Network == TCP {
			if in.Security == SecurityTLS || in.Security == SecurityReality {
				entry.Flow = "xtls-rprx-vision"
			}
		}

		clients = append(clients, entry)
	}

	settings := map[string]any{"clients": clients}
	if in.Protocol == VLESS {
		settings["decryption"] = "none"
	}

	stream := map[string]any{
		"network":  string(in.Network),
		"security": string(in.Security),
	}

	// TLS/Reality settings (same as before)
	if in.Security == SecurityTLS {
		cert := map[string]any{
			"certificateFile": in.CertFile,
			"keyFile":         in.KeyFile,
		}
		tls := map[string]any{"certificates": []any{cert}}
		if in.SNI != "" {
			tls["serverName"] = in.SNI
		}
		stream["tlsSettings"] = tls
	}

	if in.Security == SecurityReality {
		reality := map[string]any{
			"show": false,
			"dest": in.Dest,
			"xver": 0,
		}
		if len(in.ServerNames) > 0 {
			reality["serverNames"] = in.ServerNames
		}
		if in.PrivateKey != "" {
			reality["privateKey"] = in.PrivateKey
		}
		if len(in.ShortIDs) > 0 {
			reality["shortIds"] = in.ShortIDs
		}
		stream["realitySettings"] = reality
	}

	switch in.Network {
	case WS:
		ws := map[string]any{"path": in.Path}
		if in.Host != "" {
			ws["headers"] = map[string]any{"Host": in.Host}
		}
		stream["wsSettings"] = ws
	case grpcNetwork:
		stream["grpcSettings"] = map[string]any{"serviceName": in.Path}
	}

	inbound := map[string]any{
		"tag":            in.Tag(),
		"listen":         in.Listen,
		"port":           in.Port,
		"protocol":       string(in.Protocol),
		"settings":       settings,
		"streamSettings": stream,
	}

	if in.Sniffing {
		inbound["sniffing"] = map[string]any{
			"enabled":      true,
			"destOverride": []any{"http", "tls"},
		}
	}

	doc := map[string]any{"inbounds": []any{inbound}}
	return json.MarshalIndent(doc, "", "  ")
}

// clientEntryWithLevel extends clientEntry with policy level.
type clientEntryWithLevel struct {
	ID       string `json:"id,omitempty"`
	Password string `json:"password,omitempty"`
	Email    string `json:"email"`
	Flow     string `json:"flow,omitempty"`
	Level    int64  `json:"level,omitempty"`
}

func sortUsersByLevel(users []UserWithLevel) {
	// Sort by SubjectID for determinism
	for i := 0; i < len(users); i++ {
		for j := i + 1; j < len(users); j++ {
			if users[i].SubjectID > users[j].SubjectID {
				users[i], users[j] = users[j], users[i]
			}
		}
	}
}
