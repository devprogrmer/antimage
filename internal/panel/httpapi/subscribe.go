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

		// Parse service params to extract protocol config.
		var params map[string]interface{}
		if err := json.Unmarshal([]byte(paramsJSON), &params); err != nil {
			continue // Skip malformed params
		}

		// Extract inbound configuration (simplified - real implementation would
		// parse the adapter-specific params structure).
		protocol, port := d.extractInboundConfig(params)
		if protocol == "" {
			continue
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

		servers = append(servers, subscriptions.Server{
			NodeID:      nodeID,
			NodeName:    nodeName,
			NodeAddress: nodeAddress,
			ServiceID:   serviceID,
			Protocol:    protocol,
			Port:        port,
			UUID:        uuid,
			Password:    password,
			TLS:         true, // Assume TLS for now
			Network:     "tcp",
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return servers, nil
}

// extractInboundConfig parses service params to extract protocol and port.
// This is a simplified implementation - production would properly parse
// adapter-specific schemas.
func (d Deps) extractInboundConfig(params map[string]interface{}) (protocol string, port int) {
	// Try to extract protocol and port from params.
	// This is adapter-specific, so we use heuristics.

	if proto, ok := params["protocol"].(string); ok {
		protocol = proto
	}
	if p, ok := params["port"].(float64); ok {
		port = int(p)
	}

	// Fallback defaults
	if protocol == "" {
		protocol = "vless"
	}
	if port == 0 {
		port = 443
	}

	return protocol, port
}

// subscriptionRateLimiter is a global rate limiter for subscription endpoints.
// In production, this would be injected via Deps.
var subscriptionRateLimiter = subscriptions.NewSlidingWindowLimiter(10, time.Minute)
