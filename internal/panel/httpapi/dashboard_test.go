package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestDashboardOverview(t *testing.T) {
	e := newTestEnv(t)
	e.seedAdmin(t, "admin", "password123", "super_admin")
	token := e.login(t, "admin", "password123")

	res := e.get(t, "/api/v1/dashboard/overview", token)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body)
	}

	var resp map[string]any
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["nodes"]; !ok {
		t.Error("expected 'nodes' field in response")
	}
	if _, ok := resp["subjects"]; !ok {
		t.Error("expected 'subjects' field in response")
	}
	if _, ok := resp["traffic_24h"]; !ok {
		t.Error("expected 'traffic_24h' field in response")
	}
	if _, ok := resp["quota"]; !ok {
		t.Error("expected 'quota' field in response")
	}
	if _, ok := resp["computed_at"]; !ok {
		t.Error("expected 'computed_at' field in response")
	}
}

func TestDashboardOverviewUnauthenticated(t *testing.T) {
	e := newTestEnv(t)

	res := e.get(t, "/api/v1/dashboard/overview", "")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", res.Code)
	}
}

func TestDashboardTrafficChart(t *testing.T) {
	e := newTestEnv(t)
	e.seedAdmin(t, "admin", "password123", "super_admin")
	token := e.login(t, "admin", "password123")

	for _, period := range []string{"24h", "7d", "30d"} {
		t.Run(period, func(t *testing.T) {
			res := e.get(t, "/api/v1/dashboard/traffic-chart?period="+period, token)
			if res.Code != http.StatusOK {
				t.Fatalf("expected 200 for period=%s, got %d: %s", period, res.Code, res.Body)
			}

			var resp map[string]any
			if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if _, ok := resp["data_points"]; !ok {
				t.Error("expected 'data_points' field in response")
			}
			if v, ok := resp["period"]; !ok || v != period {
				t.Errorf("expected period=%q, got %v", period, v)
			}
			if _, ok := resp["granularity"]; !ok {
				t.Error("expected 'granularity' field in response")
			}
		})
	}
}

func TestDashboardTrafficChartInvalidPeriod(t *testing.T) {
	e := newTestEnv(t)
	e.seedAdmin(t, "admin", "password123", "super_admin")
	token := e.login(t, "admin", "password123")

	res := e.get(t, "/api/v1/dashboard/traffic-chart?period=invalid", token)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", res.Code)
	}
}

func TestDashboardTopUsers(t *testing.T) {
	e := newTestEnv(t)
	e.seedAdmin(t, "admin", "password123", "super_admin")
	token := e.login(t, "admin", "password123")

	res := e.get(t, "/api/v1/dashboard/top-users", token)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body)
	}

	var resp map[string]any
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["top_users"]; !ok {
		t.Error("expected 'top_users' field in response")
	}
}

func TestDashboardTopUsersLimit(t *testing.T) {
	e := newTestEnv(t)
	e.seedAdmin(t, "admin", "password123", "super_admin")
	token := e.login(t, "admin", "password123")

	// Valid limit
	res := e.get(t, "/api/v1/dashboard/top-users?limit=5", token)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200 with limit=5, got %d: %s", res.Code, res.Body)
	}

	// Invalid limit (too high)
	res = e.get(t, "/api/v1/dashboard/top-users?limit=51", token)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 with limit=51, got %d", res.Code)
	}

	// Invalid limit (zero)
	res = e.get(t, "/api/v1/dashboard/top-users?limit=0", token)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 with limit=0, got %d", res.Code)
	}
}
