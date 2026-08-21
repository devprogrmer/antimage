package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCreateServiceTemplate(t *testing.T) {
	e := newTestEnv(t)
	e.seedAdmin(t, "admin", "password123", "super_admin")
	tok := e.login(t, "admin", "password123")

	body := `{"name":"test-template","adapter_kind":"xray","params_json":"{\"key\":\"val\"}","description":"a template","tags":[],"is_public":false}`
	res := e.post(t, "/api/v1/templates/services", body, tok)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", res.Code, res.Body)
	}

	var resp map[string]any
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["id"]; !ok {
		t.Fatalf("expected 'id' in response, got: %v", resp)
	}
}

func TestListServiceTemplates(t *testing.T) {
	e := newTestEnv(t)
	e.seedAdmin(t, "admin", "password123", "super_admin")
	tok := e.login(t, "admin", "password123")

	res := e.get(t, "/api/v1/templates/services", tok)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body)
	}

	var resp map[string]any
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["templates"]; !ok {
		t.Fatalf("expected 'templates' key in response, got: %v", resp)
	}
}

func TestCreateUserPreset(t *testing.T) {
	e := newTestEnv(t)
	e.seedAdmin(t, "admin", "password123", "super_admin")
	tok := e.login(t, "admin", "password123")

	body := `{"name":"my-preset","description":"a preset","is_public":false}`
	res := e.post(t, "/api/v1/presets/users", body, tok)
	if res.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", res.Code, res.Body)
	}

	var resp map[string]any
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["id"]; !ok {
		t.Fatalf("expected 'id' in response, got: %v", resp)
	}
}

func TestListUserPresets(t *testing.T) {
	e := newTestEnv(t)
	e.seedAdmin(t, "admin", "password123", "super_admin")
	tok := e.login(t, "admin", "password123")

	res := e.get(t, "/api/v1/presets/users", tok)
	if res.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", res.Code, res.Body)
	}

	var resp map[string]any
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["presets"]; !ok {
		t.Fatalf("expected 'presets' key in response, got: %v", resp)
	}
}
