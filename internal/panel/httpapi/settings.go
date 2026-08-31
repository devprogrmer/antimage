package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

var allowedSettings = map[string]bool{
	"public_url":        true,
	"remark_template":   true,
	"brand_name":        true,
	"support_url":       true,
	"subscription_info": true,
}

func (d Deps) loadPanelSettings(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	rows, err := d.Store.Read().QueryContext(ctx, `SELECT key, value FROM panel_settings`)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func (d Deps) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	// Any signed-in operator may read non-secret settings: they need public_url
	// to copy a subscription. Writes stay behind settings:write.
	_ = actor
	settings, err := d.loadPanelSettings(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load settings")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

func (d Deps) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSettingsWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	var req struct {
		Settings map[string]string `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if len(req.Settings) == 0 {
		WriteError(w, http.StatusUnprocessableEntity, "validation", "no settings supplied")
		return
	}
	for k, v := range req.Settings {
		if !allowedSettings[k] {
			WriteError(w, http.StatusUnprocessableEntity, "validation", "unknown setting: "+k)
			return
		}
		if k == "public_url" && strings.TrimSpace(v) != "" {
			u, err := url.Parse(v)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				WriteError(w, http.StatusUnprocessableEntity, "validation", "public_url must be an http(s) URL")
				return
			}
			req.Settings[k] = strings.TrimRight(u.String(), "/")
		}
	}
	now := d.now().Unix()
	ctx := r.Context()
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		for k, v := range req.Settings {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO panel_settings (key, value, updated_at) VALUES (?,?,?)
				 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
				k, v, now); err != nil {
				return err
			}
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "settings.update", TargetType: "settings", Result: "ok",
			After: map[string]any{"keys": keysOf(req.Settings)},
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not save settings")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (d Deps) publicBaseURL(r *http.Request) string {
	settings, _ := d.loadPanelSettings(r.Context())
	if u := strings.TrimRight(strings.TrimSpace(settings["public_url"]), "/"); u != "" {
		return u
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
