package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/subjects"
)

// SubjectJSON represents a subject in API responses.
type SubjectJSON struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	SubscriptionURL string `json:"subscription_url"`
	ExpiresAt       *int64 `json:"expires_at,omitempty"`
	CreatedAt       int64  `json:"created_at"`
	Note            string `json:"note,omitempty"`
}

// handleGetSubject implements GET /api/v1/subjects/{id}.
func (d Deps) handleGetSubject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	subjectID := chi.URLParam(r, "id")

	var (
		id        int64
		name      string
		enabled   bool
		token     string
		expiresAt sql.NullInt64
		createdAt int64
		note      string
	)

	row := d.Store.Read().QueryRowContext(ctx,
		`SELECT id, name, enabled, subscription_token, expires_at, created_at, note
		 FROM subjects WHERE id = ?`, subjectID)
	err := row.Scan(&id, &name, &enabled, &token, &expiresAt, &createdAt, &note)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Ensure token exists (lazy initialization).
	if token == "" {
		token, err = subjects.EnsureToken(ctx, d.Store, id)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Build subscription URL.
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	subscriptionURL := fmt.Sprintf("%s://%s/api/v1/subscribe/%s", scheme, r.Host, token)

	resp := SubjectJSON{
		ID:              id,
		Name:            name,
		Enabled:         enabled,
		SubscriptionURL: subscriptionURL,
		CreatedAt:       createdAt,
		Note:            note,
	}
	if expiresAt.Valid {
		resp.ExpiresAt = &expiresAt.Int64
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleRevokeToken implements POST /api/v1/subjects/{id}/revoke-token.
func (d Deps) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	subjectID := chi.URLParam(r, "id")

	// Parse subject ID.
	var id int64
	if _, err := fmt.Sscanf(subjectID, "%d", &id); err != nil {
		http.Error(w, "invalid subject id", http.StatusBadRequest)
		return
	}

	// Revoke token (regenerate).
	newToken, err := subjects.RevokeToken(ctx, d.Store, id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Build new subscription URL.
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	subscriptionURL := fmt.Sprintf("%s://%s/api/v1/subscribe/%s", scheme, r.Host, newToken)

	// TODO: Audit log the revocation.

	resp := map[string]string{
		"subscription_url": subscriptionURL,
		"message":          "subscription token revoked",
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
