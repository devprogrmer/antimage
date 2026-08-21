package xray

import (
	"encoding/json"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

func TestGeneratePolicyConfig(t *testing.T) {
	t.Run("no speed limits", func(t *testing.T) {
		subjects := []adapter.Subject{
			{ID: 1, Credentials: []adapter.Credential{{Kind: "uuid", Value: "uuid-1"}}},
			{ID: 2, Credentials: []adapter.Credential{{Kind: "uuid", Value: "uuid-2"}}},
		}

		data, err := GeneratePolicyConfig(subjects)
		if err != nil {
			t.Fatalf("GeneratePolicyConfig failed: %v", err)
		}

		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("unmarshal policy: %v", err)
		}

		policy := doc["policy"].(map[string]any)
		levels := policy["levels"].(map[string]any)

		// Should have default level only
		if len(levels) != 1 {
			t.Errorf("expected 1 level (default), got %d", len(levels))
		}

		if _, ok := levels["0"]; !ok {
			t.Error("expected default level '0'")
		}
	})

	t.Run("with speed limits", func(t *testing.T) {
		upLimit := int64(1000)    // 1000 kbps = 128000 bytes/sec
		downLimit := int64(5000)  // 5000 kbps = 640000 bytes/sec

		subjects := []adapter.Subject{
			{
				ID:                 1,
				SpeedLimitUpKbps:   &upLimit,
				SpeedLimitDownKbps: &downLimit,
			},
			{
				ID: 2, // no limits
			},
		}

		data, err := GeneratePolicyConfig(subjects)
		if err != nil {
			t.Fatalf("GeneratePolicyConfig failed: %v", err)
		}

		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("unmarshal policy: %v", err)
		}

		policy := doc["policy"].(map[string]any)
		levels := policy["levels"].(map[string]any)

		// Should have default level + 1 custom level
		if len(levels) != 2 {
			t.Errorf("expected 2 levels, got %d", len(levels))
		}

		// Check subject 1's level
		level1, ok := levels["1"].(map[string]any)
		if !ok {
			t.Fatal("expected level '1' for subject 1")
		}

		// Convert kbps to bytes/sec: 1000 * 1024 / 8 = 128000
		expectedUp := float64(128000)
		if level1["upSpeed"].(float64) != expectedUp {
			t.Errorf("expected upSpeed=%v, got %v", expectedUp, level1["upSpeed"])
		}

		// 5000 * 1024 / 8 = 640000
		expectedDown := float64(640000)
		if level1["downSpeed"].(float64) != expectedDown {
			t.Errorf("expected downSpeed=%v, got %v", expectedDown, level1["downSpeed"])
		}
	})

	t.Run("mixed limits", func(t *testing.T) {
		upOnly := int64(2000)
		downOnly := int64(3000)

		subjects := []adapter.Subject{
			{ID: 1, SpeedLimitUpKbps: &upOnly},
			{ID: 2, SpeedLimitDownKbps: &downOnly},
		}

		data, err := GeneratePolicyConfig(subjects)
		if err != nil {
			t.Fatalf("GeneratePolicyConfig failed: %v", err)
		}

		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("unmarshal policy: %v", err)
		}

		policy := doc["policy"].(map[string]any)
		levels := policy["levels"].(map[string]any)

		// Default + 2 custom levels
		if len(levels) != 3 {
			t.Errorf("expected 3 levels, got %d", len(levels))
		}

		// Subject 1: up only
		level1 := levels["1"].(map[string]any)
		if _, ok := level1["upSpeed"]; !ok {
			t.Error("expected upSpeed for subject 1")
		}
		if _, ok := level1["downSpeed"]; ok {
			t.Error("unexpected downSpeed for subject 1")
		}

		// Subject 2: down only
		level2 := levels["2"].(map[string]any)
		if _, ok := level2["upSpeed"]; ok {
			t.Error("unexpected upSpeed for subject 2")
		}
		if _, ok := level2["downSpeed"]; !ok {
			t.Error("expected downSpeed for subject 2")
		}
	})
}

func TestGenerateWithPolicy(t *testing.T) {
	inbound := Inbound{
		Protocol: VLESS,
		Port:     10086,
		Listen:   "0.0.0.0",
		Network:  TCP,
		Security: SecurityNone,
	}

	t.Run("users with levels", func(t *testing.T) {
		users := []UserWithLevel{
			{User: User{SubjectID: 1, Email: "user-1@antimage", Credential: "uuid-1"}, Level: 1},
			{User: User{SubjectID: 2, Email: "user-2@antimage", Credential: "uuid-2"}, Level: 2},
			{User: User{SubjectID: 3, Email: "user-3@antimage", Credential: "uuid-3"}, Level: 3},
		}

		data, err := inbound.GenerateWithPolicy(users)
		if err != nil {
			t.Fatalf("GenerateWithPolicy failed: %v", err)
		}

		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("unmarshal config: %v", err)
		}

		inbounds := doc["inbounds"].([]any)
		if len(inbounds) != 1 {
			t.Fatalf("expected 1 inbound, got %d", len(inbounds))
		}

		inboundCfg := inbounds[0].(map[string]any)
		settings := inboundCfg["settings"].(map[string]any)
		clients := settings["clients"].([]any)

		if len(clients) != 3 {
			t.Fatalf("expected 3 clients, got %d", len(clients))
		}

		// Check levels are assigned (level field is omitted if 0, present otherwise)
		for i, client := range clients {
			c := client.(map[string]any)
			expectedLevel := users[i].Level

			if levelVal, ok := c["level"]; ok {
				level := int64(levelVal.(float64))
				if level != expectedLevel {
					t.Errorf("client %d: expected level %d, got %d", i, expectedLevel, level)
				}
			} else if expectedLevel != 0 {
				t.Errorf("client %d: expected level %d, but level field is missing", i, expectedLevel)
			}
		}
	})

	t.Run("deterministic ordering", func(t *testing.T) {
		// Users in random order
		users := []UserWithLevel{
			{User: User{SubjectID: 5, Email: "user-5@antimage", Credential: "uuid-5"}, Level: 5},
			{User: User{SubjectID: 1, Email: "user-1@antimage", Credential: "uuid-1"}, Level: 1},
			{User: User{SubjectID: 3, Email: "user-3@antimage", Credential: "uuid-3"}, Level: 3},
		}

		data1, err := inbound.GenerateWithPolicy(users)
		if err != nil {
			t.Fatalf("GenerateWithPolicy failed: %v", err)
		}

		// Shuffle and generate again
		users = []UserWithLevel{
			{User: User{SubjectID: 3, Email: "user-3@antimage", Credential: "uuid-3"}, Level: 3},
			{User: User{SubjectID: 5, Email: "user-5@antimage", Credential: "uuid-5"}, Level: 5},
			{User: User{SubjectID: 1, Email: "user-1@antimage", Credential: "uuid-1"}, Level: 1},
		}

		data2, err := inbound.GenerateWithPolicy(users)
		if err != nil {
			t.Fatalf("GenerateWithPolicy failed: %v", err)
		}

		// Should produce identical output
		if string(data1) != string(data2) {
			t.Error("GenerateWithPolicy is not deterministic")
		}
	})
}

func TestSpeedLimitConversion(t *testing.T) {
	tests := []struct {
		name     string
		kbps     int64
		expected int64
	}{
		{"1 Mbps", 1000, 128000},      // 1000 kbps = 128 KB/s
		{"10 Mbps", 10000, 1280000},   // 10000 kbps = 1.28 MB/s
		{"100 Mbps", 100000, 12800000}, // 100000 kbps = 12.8 MB/s
		{"500 kbps", 500, 64000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subjects := []adapter.Subject{
				{ID: 1, SpeedLimitUpKbps: &tt.kbps},
			}

			data, err := GeneratePolicyConfig(subjects)
			if err != nil {
				t.Fatalf("GeneratePolicyConfig failed: %v", err)
			}

			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("unmarshal policy: %v", err)
			}

			policy := doc["policy"].(map[string]any)
			levels := policy["levels"].(map[string]any)
			level1 := levels["1"].(map[string]any)

			got := int64(level1["upSpeed"].(float64))
			if got != tt.expected {
				t.Errorf("expected %d bytes/sec, got %d", tt.expected, got)
			}
		})
	}
}
