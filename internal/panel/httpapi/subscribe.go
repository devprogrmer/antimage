package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/subjects"
	"github.com/amyrm/antimage/internal/panel/subscriptions"
)

// handleSubscribe implements GET /api/v1/subscribe/{token}.
// This is a PUBLIC, UNAUTHENTICATED endpoint - the token IS the authentication.
func (d Deps) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := chi.URLParam(r, "token")

	if token == "" {
		http.Error(w, "missing token", http.StatusNotFound)
		return
	}

	// Rate limiting (10 req/min per token)
	if !subscriptionRateLimiter.Allow(token) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	// Lookup subject by token.
	subjectID, err := subjects.LookupByToken(ctx, d.Store, token)
	if err != nil {
		// Token not found or subject disabled - always return 404 for security.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Check subject eligibility (not expired, not frozen).
	var (
		expiresAt sql.NullInt64
		frozenAt  sql.NullInt64
	)
	row := d.Store.Read().QueryRowContext(ctx,
		`SELECT expires_at, frozen_at FROM subjects WHERE id = ?`, subjectID)
	if err := row.Scan(&expiresAt, &frozenAt); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	now := d.now().Unix()
	if expiresAt.Valid && expiresAt.Int64 <= now {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if frozenAt.Valid {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Query all nodes serving this subject.
	servers, err := d.gatherServers(ctx, subjectID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The subject's subscription group, if they are on one. Applied HERE
	// rather than in the SQL that gathers servers, because the protocol of an
	// Xray inbound lives inside its params -- a WHERE clause could only filter
	// by adapter kind and would treat vless and trojan as one thing.
	filter, err := subscriptions.FilterForSubject(ctx, d.Store, subjectID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	servers = filter.Apply(servers)

	if len(servers) == 0 {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	// Detect format from User-Agent.
	userAgent := r.Header.Get("User-Agent")
	format := subscriptions.DetectFormat(userAgent)

	// Render config.
	var content []byte
	var contentType string
	switch format {
	case subscriptions.FormatV2Ray:
		content, contentType, err = (&subscriptions.V2RayRenderer{}).Render(ctx, servers)
	case subscriptions.FormatClash:
		content, contentType, err = (&subscriptions.ClashRenderer{}).Render(ctx, servers)
	case subscriptions.FormatSingBox:
		content, contentType, err = (&subscriptions.SingBoxRenderer{}).Render(ctx, servers)
	default:
		content, contentType, err = (&subscriptions.V2RayRenderer{}).Render(ctx, servers)
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Audit log (token redacted for security).
	// TODO: wire audit logging when available.

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// gatherServers queries all nodes and credentials for a subject.
// This is a simplified implementation - in production, this would parse service params
// and properly extract inbound configurations.
func (d Deps) gatherServers(ctx context.Context, subjectID int64) ([]subscriptions.Server, error) {
	// Query nodes serving this subject via subject_services.
	query := `
		SELECT
			n.id, n.name, n.address,
			s.id, s.adapter_kind, s.params,
			sc.kind, sc.value_enc
		FROM subjects sub
		JOIN subject_services ss ON ss.subject_id = sub.id
		JOIN services s ON s.id = ss.service_id
		JOIN nodes n ON n.id = s.node_id
		LEFT JOIN subject_credentials sc ON sc.subject_id = sub.id
		WHERE sub.id = ?
		  AND sub.enabled = 1
		  AND n.status = 'online'
		  AND s.enabled = 1
	`

	rows, err := d.Store.Read().QueryContext(ctx, query, subjectID)
	if err != nil {
		return nil, fmt.Errorf("query servers: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var servers []subscriptions.Server
	credsByKind := make(map[string][]byte)

	for rows.Next() {
		var (
			nodeID      int64
			nodeName    string
			nodeAddress string
			serviceID   int64
			adapterKind string
			paramsJSON  string
			credKind    sql.NullString
			credEnc     []byte
		)

		err := rows.Scan(&nodeID, &nodeName, &nodeAddress,
			&serviceID, &adapterKind, &paramsJSON,
			&credKind, &credEnc)
		if err != nil {
			return nil, fmt.Errorf("scan server: %w", err)
		}

		// Collect credentials (unsealing deferred).
		if credKind.Valid && len(credEnc) > 0 {
			credsByKind[credKind.String] = credEnc
		}

		var params map[string]interface{}
		if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
			continue // Skip malformed params
		}

		// Unseal credentials.
		var uuid, password string
		if d.Box != nil {
			if encUUID, ok := credsByKind["uuid"]; ok {
				if plain, err := d.Box.Open(encUUID); err == nil {
					uuid = string(plain)
				}
			}
			if encPass, ok := credsByKind["password"]; ok {
				if plain, err := d.Box.Open(encPass); err == nil {
					password = string(plain)
				}
			}
		}

		// ONE mapper, shared with the per-inbound panel. This used to read a
		// "protocol" key that only Xray and sing-box have and default a missing
		// one to "vless", so every WireGuard, OpenVPN, ocserv and L2TP inbound
		// was emitted into the subscription as a VLESS entry pointing at its
		// port. It also hardcoded TLS and TCP, so a plaintext WebSocket inbound
		// produced an entry that could not connect.
		srv, err := subscriptions.ServerFromInbound(
			subscriptions.Inbound{
				ServiceID: serviceID, AdapterKind: adapterKind, Params: params,
			},
			subscriptions.NodeRef{ID: nodeID, Name: nodeName, Address: nodeAddress},
			subscriptions.Credentials{UUID: uuid, Password: password},
		)
		if err != nil {
			// Not representable here, or unreadable. Skipped rather than
			// guessed at; the panel's per-inbound view is where the operator
			// sees which inbounds an aggregated format cannot carry.
			continue
		}
		servers = append(servers, srv)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return servers, nil
}

// subscriptionRateLimiter is a global rate limiter for subscription endpoints.
// In production, this would be injected via Deps.
var subscriptionRateLimiter = subscriptions.NewSlidingWindowLimiter(10, time.Minute)
