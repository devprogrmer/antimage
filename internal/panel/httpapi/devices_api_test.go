package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestDeviceEndpointsRequireAuth(t *testing.T) {
	env := newTestEnv(t)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"list devices", "GET", "/api/v1/subjects/1/devices"},
		{"list connections", "GET", "/api/v1/subjects/1/connections"},
		{"get enforcement", "GET", "/api/v1/subjects/1/enforcement"},
		{"revoke device", "POST", "/api/v1/devices/1/revoke"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := env.do(t, tt.method, tt.path, `{"reason":"test"}`, "")

			if w.Code != http.StatusUnauthorized {
				t.Errorf("expected 401 Unauthorized, got %d", w.Code)
			}
		})
	}
}

func TestDeviceEndpointsRespectRBAC(t *testing.T) {
	env := newTestEnv(t)

	// Create admin with limited permissions (node:read only, no subject:read)
	env.seedAdmin(t, "limited", "password", "readonly")
	// Modify the role to remove subject:read
	ctx := context.Background()
	_ = env.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE roles SET permissions = '["node:read"]' WHERE name = 'readonly'`)
		return err
	})

	token := env.login(t, "limited", "password")

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"list devices", "GET", "/api/v1/subjects/1/devices"},
		{"list connections", "GET", "/api/v1/subjects/1/connections"},
		{"get enforcement", "GET", "/api/v1/subjects/1/enforcement"},
		{"revoke device", "POST", "/api/v1/devices/1/revoke"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := env.do(t, tt.method, tt.path, `{"reason":"test"}`, token)

			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403 Forbidden, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDeviceEndpointsPagination(t *testing.T) {
	env := newTestEnv(t)
	env.seedAdmin(t, "admin", "password", "super_admin")
	token := env.login(t, "admin", "password")

	tests := []struct {
		name string
		path string
	}{
		{"default pagination", "/api/v1/subjects/1/devices"},
		{"custom limit", "/api/v1/subjects/1/devices?limit=50"},
		{"max limit capped", "/api/v1/subjects/1/devices?limit=2000"},
		{"with offset", "/api/v1/subjects/1/devices?limit=10&offset=5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := env.get(t, tt.path, token)

			if w.Code != http.StatusOK {
				t.Errorf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
			}

			var devices []DeviceResponse
			if err := json.NewDecoder(w.Body).Decode(&devices); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if devices == nil {
				t.Error("expected non-nil devices array")
			}
		})
	}
}

func TestRateLimiter(t *testing.T) {
	limiter := newRateLimiter(5, 100*time.Millisecond)

	adminID := int64(1)

	// Should allow first 5 requests
	for i := 0; i < 5; i++ {
		if !limiter.allow(adminID) {
			t.Errorf("request %d should be allowed", i)
		}
	}

	// 6th request should be blocked
	if limiter.allow(adminID) {
		t.Error("6th request should be blocked")
	}

	// Wait for window to reset
	time.Sleep(110 * time.Millisecond)

	// Should allow again
	if !limiter.allow(adminID) {
		t.Error("request after window reset should be allowed")
	}
}

func TestRateLimiterPerAdmin(t *testing.T) {
	limiter := newRateLimiter(2, 100*time.Millisecond)

	admin1 := int64(1)
	admin2 := int64(2)

	// Admin 1 uses their quota
	limiter.allow(admin1)
	limiter.allow(admin1)

	// Admin 1 blocked
	if limiter.allow(admin1) {
		t.Error("admin 1 should be blocked")
	}

	// Admin 2 should still have quota
	if !limiter.allow(admin2) {
		t.Error("admin 2 should be allowed")
	}
}
