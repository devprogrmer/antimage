package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/devices"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/go-chi/chi/v5"
)

// DeviceResponse represents a device in API responses.
type DeviceResponse struct {
	ID            int64  `json:"id"`
	SubjectID     int64  `json:"subject_id"`
	HWID          string `json:"hwid"`
	Name          string `json:"name"`
	FirstSeenAt   int64  `json:"first_seen_at"`
	LastSeenAt    int64  `json:"last_seen_at"`
	LastIP        string `json:"last_ip"`
	UserAgent     string `json:"user_agent"`
	IsActive      bool   `json:"is_active"`
	RevokedAt     *int64 `json:"revoked_at,omitempty"`
	RevokedReason string `json:"revoked_reason,omitempty"`
}

// ActiveConnectionResponse represents an active connection.
type ActiveConnectionResponse struct {
	SubjectID    int64  `json:"subject_id"`
	DeviceID     *int64 `json:"device_id,omitempty"`
	NodeID       int64  `json:"node_id"`
	ConnectionID string `json:"connection_id"`
	SourceIP     string `json:"source_ip"`
	ConnectedAt  int64  `json:"connected_at"`
	LastSeenAt   int64  `json:"last_seen_at"`
	ProtocolInfo string `json:"protocol_info"`
}

// EnforcementStatusResponse represents enforcement status for a subject.
type EnforcementStatusResponse struct {
	SubjectID          int64  `json:"subject_id"`
	MaxDevices         *int64 `json:"max_devices,omitempty"`
	MaxIPs             *int64 `json:"max_ips,omitempty"`
	MaxConnections     *int64 `json:"max_connections,omitempty"`
	SpeedLimitUpKbps   *int64 `json:"speed_limit_up_kbps,omitempty"`
	SpeedLimitDownKbps *int64 `json:"speed_limit_down_kbps,omitempty"`
	CurrentDevices     int    `json:"current_devices"`
	CurrentIPs         int    `json:"current_ips"`
	CurrentConnections int    `json:"current_connections"`
}

// handleListDevices lists all devices for a subject.
// GET /api/subjects/:id/devices
func (d Deps) handleListDevices(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())
	if err := rbac.Check(actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		WriteError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return
	}

	subjectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid subject ID", http.StatusBadRequest)
		return
	}

	// Parse pagination parameters
	limit := 100 // default
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 && parsedLimit <= 1000 {
			limit = parsedLimit
		}
	}

	offset := 0
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if parsedOffset, err := strconv.Atoi(offsetStr); err == nil && parsedOffset >= 0 {
			offset = parsedOffset
		}
	}

	deviceStore := devices.NewStore(d.Store, nil)
	devs, err := deviceStore.ListDevicesPaginated(r.Context(), subjectID, limit, offset)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to list devices")
		return
	}

	response := make([]DeviceResponse, 0, len(devs))
	for _, dev := range devs {
		dr := DeviceResponse{
			ID:            dev.ID,
			SubjectID:     dev.SubjectID,
			HWID:          dev.HWID,
			Name:          dev.Name,
			FirstSeenAt:   dev.FirstSeenAt.Unix(),
			LastSeenAt:    dev.LastSeenAt.Unix(),
			LastIP:        dev.LastIP,
			UserAgent:     dev.UserAgent,
			IsActive:      dev.IsActive,
			RevokedReason: dev.RevokedReason,
		}
		if dev.RevokedAt != nil {
			revoked := dev.RevokedAt.Unix()
			dr.RevokedAt = &revoked
		}
		response = append(response, dr)
	}

	WriteJSON(w, http.StatusOK, response)
}

// handleRevokeDevice revokes a device.
// POST /api/devices/:id/revoke
func (d Deps) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())
	if err := rbac.Check(actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		WriteError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return
	}

	deviceID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Reason == "" {
		req.Reason = "revoked by administrator"
	}

	deviceStore := devices.NewStore(d.Store, nil)
	ctx := r.Context()

	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		return deviceStore.RevokeDevice(ctx, tx, deviceID, req.Reason)
	})

	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleListActiveConnections lists active connections for a subject.
// GET /api/subjects/:id/connections
func (d Deps) handleListActiveConnections(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())
	if err := rbac.Check(actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		WriteError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return
	}

	subjectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid subject ID", http.StatusBadRequest)
		return
	}

	// Parse pagination parameters
	limit := 100 // default
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 && parsedLimit <= 1000 {
			limit = parsedLimit
		}
	}

	offset := 0
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if parsedOffset, err := strconv.Atoi(offsetStr); err == nil && parsedOffset >= 0 {
			offset = parsedOffset
		}
	}

	ctx := r.Context()
	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT subject_id, device_id, node_id, connection_id, source_ip, connected_at, last_seen_at, protocol_info
		 FROM active_connections
		 WHERE subject_id = ?
		 ORDER BY connected_at DESC
		 LIMIT ? OFFSET ?`,
		subjectID, limit, offset)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to list connections")
		return
	}
	defer rows.Close()

	var connections []ActiveConnectionResponse
	for rows.Next() {
		var c ActiveConnectionResponse
		var deviceID sql.NullInt64

		err := rows.Scan(&c.SubjectID, &deviceID, &c.NodeID, &c.ConnectionID,
			&c.SourceIP, &c.ConnectedAt, &c.LastSeenAt, &c.ProtocolInfo)
		if err != nil {
			continue
		}

		if deviceID.Valid {
			c.DeviceID = &deviceID.Int64
		}

		connections = append(connections, c)
	}

	if connections == nil {
		connections = []ActiveConnectionResponse{}
	}

	WriteJSON(w, http.StatusOK, connections)
}

// handleGetEnforcementStatus returns enforcement status for a subject.
// GET /api/subjects/:id/enforcement
func (d Deps) handleGetEnforcementStatus(w http.ResponseWriter, r *http.Request) {
	actor := ActorFrom(r.Context())
	if err := rbac.Check(actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}); err != nil {
		WriteError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
		return
	}

	subjectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid subject ID", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	var status EnforcementStatusResponse
	status.SubjectID = subjectID

	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT
			s.max_devices,
			s.max_ips,
			s.max_connections,
			s.speed_limit_up_kbps,
			s.speed_limit_down_kbps,
			(SELECT COUNT(*) FROM subject_devices WHERE subject_id = s.id AND revoked_at IS NULL) as device_count,
			(SELECT COUNT(DISTINCT source_ip) FROM active_connections WHERE subject_id = s.id) as ip_count,
			(SELECT COUNT(*) FROM active_connections WHERE subject_id = s.id) as conn_count
		 FROM subjects s
		 WHERE s.id = ?`,
		subjectID).Scan(
		&status.MaxDevices,
		&status.MaxIPs,
		&status.MaxConnections,
		&status.SpeedLimitUpKbps,
		&status.SpeedLimitDownKbps,
		&status.CurrentDevices,
		&status.CurrentIPs,
		&status.CurrentConnections,
	)

	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "failed to get enforcement status")
		return
	}

	WriteJSON(w, http.StatusOK, status)
}
