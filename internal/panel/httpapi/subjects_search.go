package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type SubjectListV2Response struct {
	Subjects []subjectDTO `json:"subjects"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

func (d Deps) handleListSubjectsV2(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := strconv.Atoi(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	pageSize := 50
	if ps := r.URL.Query().Get("page_size"); ps != "" {
		if parsed, err := strconv.Atoi(ps); err == nil && parsed > 0 && parsed <= 1000 {
			pageSize = parsed
		}
	}
	offset := (page - 1) * pageSize

	search := strings.TrimSpace(r.URL.Query().Get("search"))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	expiresBeforeStr := strings.TrimSpace(r.URL.Query().Get("expires_before"))
	expiresAfterStr := strings.TrimSpace(r.URL.Query().Get("expires_after"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	serviceIDStr := strings.TrimSpace(r.URL.Query().Get("service_id"))
	nodeIDStr := strings.TrimSpace(r.URL.Query().Get("node_id"))
	ownerIDStr := strings.TrimSpace(r.URL.Query().Get("owner_id"))
	protocolFilter := strings.TrimSpace(r.URL.Query().Get("protocol"))
	idStr := strings.TrimSpace(r.URL.Query().Get("id"))

	conditions := []string{store.SubjectScopeSQL}
	args := append([]any{}, store.ScopeArgs(rbac.ScopeOf(actor))...)

	if search != "" {
		if _, err := strconv.ParseInt(search, 10, 64); err == nil {
			conditions = append(conditions, "(subjects.name LIKE ? OR subjects.note LIKE ? OR subjects.id = ?)")
			searchPattern := "%" + search + "%"
			args = append(args, searchPattern, searchPattern, search)
		} else {
			conditions = append(conditions, "(subjects.name LIKE ? OR subjects.note LIKE ? OR subjects.telegram_id LIKE ?)")
			searchPattern := "%" + search + "%"
			args = append(args, searchPattern, searchPattern, searchPattern)
		}
	}

	if idStr != "" {
		if parsed, err := strconv.ParseInt(idStr, 10, 64); err == nil {
			conditions = append(conditions, "subjects.id = ?")
			args = append(args, parsed)
		}
	}

	if statusFilter != "" {
		switch statusFilter {
		case "active":
			conditions = append(conditions, "subjects.enabled = 1 AND subjects.frozen_at IS NULL AND (subjects.expires_at IS NULL OR subjects.expires_at > ?)")
			args = append(args, time.Now().Unix())
		case "disabled":
			conditions = append(conditions, "subjects.enabled = 0")
		case "frozen":
			conditions = append(conditions, "subjects.frozen_at IS NOT NULL")
		case "expired":
			conditions = append(conditions, "(subjects.expired_at IS NOT NULL OR (subjects.expires_at IS NOT NULL AND subjects.expires_at <= ?))")
			args = append(args, time.Now().Unix())
		case "limited":
			conditions = append(conditions, "subjects.quota_bytes IS NOT NULL AND subjects.quota_used_bytes >= subjects.quota_bytes")
		case "expiring_soon":
			soon := time.Now().Add(7 * 24 * time.Hour).Unix()
			conditions = append(conditions, "subjects.expires_at IS NOT NULL AND subjects.expires_at > ? AND subjects.expires_at <= ?")
			args = append(args, time.Now().Unix(), soon)
		case "online":
			conditions = append(conditions, "subjects.is_online = 1")
		case "offline":
			conditions = append(conditions, "subjects.is_online = 0")
		case "on_hold":
			conditions = append(conditions, "subjects.status = 'on_hold'")
		}
	}

	if expiresBeforeStr != "" {
		if t, err := time.Parse("2006-01-02", expiresBeforeStr); err == nil {
			conditions = append(conditions, "subjects.expires_at IS NOT NULL AND subjects.expires_at <= ?")
			args = append(args, t.Unix())
		}
	}
	if expiresAfterStr != "" {
		if t, err := time.Parse("2006-01-02", expiresAfterStr); err == nil {
			conditions = append(conditions, "subjects.expires_at IS NOT NULL AND subjects.expires_at >= ?")
			args = append(args, t.Unix())
		}
	}

	if serviceIDStr != "" {
		if sid, err := strconv.ParseInt(serviceIDStr, 10, 64); err == nil {
			conditions = append(conditions, "subjects.id IN (SELECT subject_id FROM subject_services WHERE service_id = ?)")
			args = append(args, sid)
		}
	}
	if nodeIDStr != "" {
		if nid, err := strconv.ParseInt(nodeIDStr, 10, 64); err == nil {
			conditions = append(conditions, "subjects.id IN (SELECT ss.subject_id FROM subject_services ss JOIN services s ON s.id = ss.service_id WHERE s.node_id = ?)")
			args = append(args, nid)
		}
	}
	if ownerIDStr != "" {
		if oid, err := strconv.ParseInt(ownerIDStr, 10, 64); err == nil {
			conditions = append(conditions, "(subjects.owner_admin_id = ? OR subjects.id IN (SELECT rs.subject_id FROM reseller_subjects rs JOIN resellers r ON r.id = rs.reseller_id WHERE r.admin_id = ?))")
			args = append(args, oid, oid)
		}
	}
	if protocolFilter != "" {
		conditions = append(conditions, "subjects.id IN (SELECT ss.subject_id FROM subject_services ss JOIN services s ON s.id = ss.service_id WHERE s.adapter_kind = ? OR json_extract(s.params, '$.protocol') = ?)")
		args = append(args, protocolFilter, protocolFilter)
	}

	trafficMin := strings.TrimSpace(r.URL.Query().Get("traffic_min"))
	if trafficMin != "" {
		if parsed, err := strconv.ParseInt(trafficMin, 10, 64); err == nil && parsed >= 0 {
			conditions = append(conditions, "subjects.quota_used_bytes >= ?")
			args = append(args, parsed)
		}
	}
	trafficMax := strings.TrimSpace(r.URL.Query().Get("traffic_max"))
	if trafficMax != "" {
		if parsed, err := strconv.ParseInt(trafficMax, 10, 64); err == nil && parsed >= 0 {
			conditions = append(conditions, "subjects.quota_used_bytes <= ?")
			args = append(args, parsed)
		}
	}

	quotaStatus := strings.TrimSpace(r.URL.Query().Get("quota_status"))
	switch quotaStatus {
	case "under_limit":
		conditions = append(conditions, "subjects.quota_bytes IS NOT NULL AND subjects.quota_used_bytes < subjects.quota_bytes")
	case "near_limit":
		conditions = append(conditions, "subjects.quota_bytes IS NOT NULL AND subjects.quota_used_bytes >= subjects.quota_bytes * 0.8 AND subjects.quota_used_bytes < subjects.quota_bytes")
	case "over_limit":
		conditions = append(conditions, "subjects.quota_bytes IS NOT NULL AND subjects.quota_used_bytes >= subjects.quota_bytes")
	case "unlimited":
		conditions = append(conditions, "subjects.quota_bytes IS NULL")
	}

	if tag != "" {
		conditions = append(conditions, "subjects.note LIKE ?")
		args = append(args, "%"+tag+"%")
	}

	whereClause := strings.Join(conditions, " AND ")

	countQuery := "SELECT COUNT(*) FROM subjects WHERE " + whereClause
	var total int
	if err := d.Store.Read().QueryRowContext(r.Context(), countQuery, args...).Scan(&total); err != nil {
		http.Error(w, "failed to count subjects: "+err.Error(), http.StatusInternalServerError)
		return
	}

	sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
	sortOrder := strings.TrimSpace(r.URL.Query().Get("order"))
	orderClause := "ORDER BY subjects.created_at DESC"
	if sortBy != "" {
		validSorts := map[string]string{
			"name":        "subjects.name",
			"created":     "subjects.created_at",
			"expires":     "subjects.expires_at",
			"traffic":     "subjects.quota_used_bytes",
			"quota":       "subjects.quota_bytes",
			"last_online": "subjects.last_online_at",
			"lifetime":    "subjects.lifetime_used_bytes",
			"owner":       "subjects.owner_admin_id",
			"id":          "subjects.id",
		}
		if col, ok := validSorts[sortBy]; ok {
			dir := "DESC"
			if sortOrder == "asc" {
				dir = "ASC"
			}
			orderClause = "ORDER BY " + col + " " + dir
		}
	}

	baseQuery := `SELECT subjects.id, subjects.name, subjects.note, subjects.enabled,
		subjects.expires_at, subjects.expired_at, subjects.created_at,
		subjects.quota_bytes, subjects.quota_used_bytes, subjects.frozen_at,
		subjects.max_devices, subjects.max_ips, subjects.max_connections,
		subjects.auto_delete_in_days, subjects.data_limit_reset_strategy,
		subjects.on_hold_expire_duration, subjects.on_hold_expires_at,
		subjects.status, subjects.lifetime_used_bytes,
		subjects.telegram_id, subjects.contact_number,
		subjects.last_online_at, subjects.is_online,
		subjects.owner_admin_id, subjects.primary_service_id
		FROM subjects WHERE ` + whereClause + " " + orderClause + " LIMIT ? OFFSET ?"

	argsPaged := append(args, pageSize, offset)
	rows, err := d.Store.Read().QueryContext(r.Context(), baseQuery, argsPaged...)
	if err != nil {
		http.Error(w, "failed to query subjects: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	out := []subjectDTO{}
	for rows.Next() {
		var id, createdAt, quotaUsed, lifetime int64
		var name, note string
		var enabled, isOnline int
		var expiresAt, expiredAt, quotaBytes, frozenAt, maxDevices, maxIPs, maxConn, autoDel, onHoldDur, onHoldExp, lastOnline, ownerID, primarySID sql.NullInt64
		var resetStrat, status, telegram, contact sql.NullString

		err := rows.Scan(&id, &name, &note, &enabled,
			&expiresAt, &expiredAt, &createdAt,
			&quotaBytes, &quotaUsed, &frozenAt,
			&maxDevices, &maxIPs, &maxConn,
			&autoDel, &resetStrat,
			&onHoldDur, &onHoldExp,
			&status, &lifetime,
			&telegram, &contact,
			&lastOnline, &isOnline,
			&ownerID, &primarySID)
		if err != nil {
			continue
		}
		dto := subjectDTO{
			ID: id, Name: name, Note: note, Enabled: enabled == 1,
			CreatedAt: createdAt, QuotaUsedBytes: quotaUsed, LifetimeUsedBytes: lifetime,
			IsOnline: isOnline == 1, Frozen: frozenAt.Valid,
		}
		if expiresAt.Valid {
			v := expiresAt.Int64
			dto.ExpiresAt = &v
			now := time.Now().Unix()
			rem := (v - now) / 86400
			dto.RemainingDays = &rem
		}
		if expiredAt.Valid {
			v := expiredAt.Int64
			dto.ExpiredAt = &v
		}
		if quotaBytes.Valid {
			v := quotaBytes.Int64
			dto.QuotaBytes = &v
			rem := v - quotaUsed
			if rem < 0 {
				rem = 0
			}
			dto.RemainingBytes = &rem
		}
		if maxDevices.Valid {
			v := maxDevices.Int64
			dto.MaxDevices = &v
		}
		if maxIPs.Valid {
			v := maxIPs.Int64
			dto.MaxIPs = &v
		}
		if maxConn.Valid {
			v := maxConn.Int64
			dto.MaxConnections = &v
		}
		if autoDel.Valid {
			v := autoDel.Int64
			dto.AutoDeleteInDays = &v
		}
		if resetStrat.Valid {
			dto.DataLimitResetStrategy = resetStrat.String
		} else {
			dto.DataLimitResetStrategy = "no_reset"
		}
		if onHoldDur.Valid {
			v := onHoldDur.Int64
			dto.OnHoldExpireDuration = &v
		}
		if onHoldExp.Valid {
			v := onHoldExp.Int64
			dto.OnHoldExpiresAt = &v
		}
		if status.Valid {
			v := status.String
			dto.Status = &v
		}
		if telegram.Valid {
			v := telegram.String
			dto.TelegramID = &v
		}
		if contact.Valid {
			v := contact.String
			dto.ContactNumber = &v
		}
		if lastOnline.Valid {
			v := lastOnline.Int64
			dto.LastOnlineAt = &v
		}
		if ownerID.Valid {
			v := ownerID.Int64
			dto.OwnerAdminID = &v
		}
		if primarySID.Valid {
			v := primarySID.Int64
			dto.PrimaryServiceID = &v
		}
		out = append(out, dto)
	}

	resp := SubjectListV2Response{
		Subjects: out,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
