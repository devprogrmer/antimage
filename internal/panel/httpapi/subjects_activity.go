package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// ActivityEvent represents a subject activity event.
type ActivityEvent struct {
	ID         int64  `json:"id"`
	SubjectID  int64  `json:"subject_id"`
	EventType  string `json:"event_type"`
	Timestamp  int64  `json:"timestamp"`
	Details    string `json:"details,omitempty"`
	IPAddress  string `json:"ip_address,omitempty"`
	DeviceID   string `json:"device_id,omitempty"`
	NodeID     *int64 `json:"node_id,omitempty"`
	BytesUp    int64  `json:"bytes_up"`
	BytesDown  int64  `json:"bytes_down"`
}

// ActivityListResponse contains activity events with pagination.
type ActivityListResponse struct {
	Activities []ActivityEvent `json:"activities"`
	Total      int             `json:"total"`
	HasMore    bool            `json:"has_more"`
}

// handleSubjectActivity returns activity history for a subject.
// GET /api/v1/subjects/{id}/activity
func (d Deps) handleSubjectActivity(w http.ResponseWriter, r *http.Request) {
	subjectIDStr := chi.URLParam(r, "id")
	subjectID, err := strconv.ParseInt(subjectIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid subject ID", http.StatusBadRequest)
		return
	}

	query := r.URL.Query()

	limit := 100
	if l := query.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	offset := 0
	if o := query.Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	fromTime := int64(0)
	if f := query.Get("from"); f != "" {
		if t, err := time.Parse(time.RFC3339, f); err == nil {
			fromTime = t.Unix()
		}
	}

	toTime := time.Now().Unix()
	if t := query.Get("to"); t != "" {
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			toTime = parsed.Unix()
		}
	}

	eventType := query.Get("event_type")

	conditions := []string{"subject_id = ?"}
	args := []interface{}{subjectID}

	if fromTime > 0 {
		conditions = append(conditions, "timestamp >= ?")
		args = append(args, fromTime)
	}

	if toTime > 0 {
		conditions = append(conditions, "timestamp <= ?")
		args = append(args, toTime)
	}

	if eventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, eventType)
	}

	whereClause := ""
	for i, cond := range conditions {
		if i == 0 {
			whereClause = "WHERE " + cond
		} else {
			whereClause += " AND " + cond
		}
	}

	countQuery := "SELECT COUNT(*) FROM subject_activity " + whereClause
	var total int
	err = d.Store.Read().QueryRowContext(r.Context(), countQuery, args...).Scan(&total)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	dataQuery := `
		SELECT id, subject_id, event_type, timestamp, details, ip_address, device_id, node_id, bytes_up, bytes_down
		FROM subject_activity ` + whereClause + ` ORDER BY timestamp DESC LIMIT ? OFFSET ?`

	queryArgs := append(args, limit, offset)
	rows, err := d.Store.Read().QueryContext(r.Context(), dataQuery, queryArgs...)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	activities := []ActivityEvent{}
	for rows.Next() {
		var a ActivityEvent
		var details, ipAddress, deviceID sql.NullString
		var nodeID sql.NullInt64
		err := rows.Scan(&a.ID, &a.SubjectID, &a.EventType, &a.Timestamp, &details, &ipAddress, &deviceID, &nodeID, &a.BytesUp, &a.BytesDown)
		if err != nil {
			continue
		}
		if details.Valid {
			a.Details = details.String
		}
		if ipAddress.Valid {
			a.IPAddress = ipAddress.String
		}
		if deviceID.Valid {
			a.DeviceID = deviceID.String
		}
		if nodeID.Valid {
			a.NodeID = &nodeID.Int64
		}
		activities = append(activities, a)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	hasMore := (offset + limit) < total

	resp := ActivityListResponse{
		Activities: activities,
		Total:      total,
		HasMore:    hasMore,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// ConnectionSummary represents an active or past connection.
type ConnectionSummary struct {
	ID           int64  `json:"id"`
	SubjectID    int64  `json:"subject_id"`
	StartTime    int64  `json:"start_time"`
	EndTime      *int64 `json:"end_time,omitempty"`
	Duration     int64  `json:"duration"` // seconds
	BytesUp      int64  `json:"bytes_up"`
	BytesDown    int64  `json:"bytes_down"`
	IPAddress    string `json:"ip_address,omitempty"`
	DeviceID     string `json:"device_id,omitempty"`
	NodeID       *int64 `json:"node_id,omitempty"`
	Protocol     string `json:"protocol,omitempty"`
}

// ConnectionListResponse contains connection history with pagination.
type ConnectionListResponse struct {
	Connections []ConnectionSummary `json:"connections"`
	Total       int                 `json:"total"`
	HasMore     bool                `json:"has_more"`
}

// handleSubjectConnections returns connection history for a subject.
// GET /api/v1/subjects/{id}/connections
func (d Deps) handleSubjectConnections(w http.ResponseWriter, r *http.Request) {
	subjectIDStr := chi.URLParam(r, "id")
	subjectID, err := strconv.ParseInt(subjectIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid subject ID", http.StatusBadRequest)
		return
	}

	query := r.URL.Query()

	limit := 100
	if l := query.Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	offset := 0
	if o := query.Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// Build connections from activity events
	// connection_start and connection_end events paired by device_id
	dataQuery := `
		WITH connection_starts AS (
			SELECT id, subject_id, timestamp as start_time, ip_address, device_id, node_id, details
			FROM subject_activity
			WHERE subject_id = ? AND event_type = 'connection_start'
		),
		connection_ends AS (
			SELECT id, subject_id, timestamp as end_time, device_id, bytes_up, bytes_down
			FROM subject_activity
			WHERE subject_id = ? AND event_type = 'connection_end'
		)
		SELECT
			s.id, s.subject_id, s.start_time, e.end_time,
			COALESCE(e.end_time - s.start_time, ? - s.start_time) as duration,
			COALESCE(e.bytes_up, 0) as bytes_up,
			COALESCE(e.bytes_down, 0) as bytes_down,
			s.ip_address, s.device_id, s.node_id, s.details
		FROM connection_starts s
		LEFT JOIN connection_ends e ON s.device_id = e.device_id AND e.end_time > s.start_time
		ORDER BY s.start_time DESC
		LIMIT ? OFFSET ?`

	now := time.Now().Unix()
	rows, err := d.Store.Read().QueryContext(r.Context(), dataQuery, subjectID, subjectID, now, limit, offset)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	connections := []ConnectionSummary{}
	for rows.Next() {
		var c ConnectionSummary
		var endTime sql.NullInt64
		var ipAddress, deviceID, details sql.NullString
		var nodeID sql.NullInt64
		err := rows.Scan(&c.ID, &c.SubjectID, &c.StartTime, &endTime, &c.Duration, &c.BytesUp, &c.BytesDown, &ipAddress, &deviceID, &nodeID, &details)
		if err != nil {
			continue
		}
		if endTime.Valid {
			c.EndTime = &endTime.Int64
		}
		if ipAddress.Valid {
			c.IPAddress = ipAddress.String
		}
		if deviceID.Valid {
			c.DeviceID = deviceID.String
		}
		if nodeID.Valid {
			c.NodeID = &nodeID.Int64
		}
		if details.Valid {
			// Extract protocol from JSON details
			var d map[string]interface{}
			if err := json.Unmarshal([]byte(details.String), &d); err == nil {
				if proto, ok := d["protocol"].(string); ok {
					c.Protocol = proto
				}
			}
		}
		connections = append(connections, c)
	}

	countQuery := `SELECT COUNT(*) FROM subject_activity WHERE subject_id = ? AND event_type = 'connection_start'`
	var total int
	_ = d.Store.Read().QueryRowContext(r.Context(), countQuery, subjectID).Scan(&total)

	hasMore := (offset + limit) < total

	resp := ConnectionListResponse{
		Connections: connections,
		Total:       total,
		HasMore:     hasMore,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// DeviceSummary represents a device that connected to the subject.
type DeviceSummary struct {
	DeviceID      string `json:"device_id"`
	FirstSeen     int64  `json:"first_seen"`
	LastSeen      int64  `json:"last_seen"`
	ConnectionCount int  `json:"connection_count"`
	TotalBytesUp  int64  `json:"total_bytes_up"`
	TotalBytesDown int64 `json:"total_bytes_down"`
	LastIPAddress string `json:"last_ip_address,omitempty"`
}

// DeviceListResponse contains device history.
type DeviceListResponse struct {
	Devices []DeviceSummary `json:"devices"`
}

// handleSubjectDevices returns device history for a subject.
// GET /api/v1/subjects/{id}/devices
func (d Deps) handleSubjectDevices(w http.ResponseWriter, r *http.Request) {
	subjectIDStr := chi.URLParam(r, "id")
	subjectID, err := strconv.ParseInt(subjectIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid subject ID", http.StatusBadRequest)
		return
	}

	dataQuery := `
		SELECT
			device_id,
			MIN(timestamp) as first_seen,
			MAX(timestamp) as last_seen,
			COUNT(*) as connection_count,
			SUM(bytes_up) as total_bytes_up,
			SUM(bytes_down) as total_bytes_down,
			(SELECT ip_address FROM subject_activity WHERE subject_id = ? AND device_id = sa.device_id ORDER BY timestamp DESC LIMIT 1) as last_ip
		FROM subject_activity sa
		WHERE subject_id = ? AND device_id IS NOT NULL
		GROUP BY device_id
		ORDER BY last_seen DESC`

	rows, err := d.Store.Read().QueryContext(r.Context(), dataQuery, subjectID, subjectID)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	devices := []DeviceSummary{}
	for rows.Next() {
		var d DeviceSummary
		var lastIP sql.NullString
		err := rows.Scan(&d.DeviceID, &d.FirstSeen, &d.LastSeen, &d.ConnectionCount, &d.TotalBytesUp, &d.TotalBytesDown, &lastIP)
		if err != nil {
			continue
		}
		if lastIP.Valid {
			d.LastIPAddress = lastIP.String
		}
		devices = append(devices, d)
	}

	resp := DeviceListResponse{
		Devices: devices,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
