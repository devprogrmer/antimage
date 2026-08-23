package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SubjectListV2Response includes pagination metadata.
type SubjectListV2Response struct {
	Subjects []subjectDTO `json:"subjects"`
	Total    int          `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

// handleListSubjectsV2 returns a paginated, searchable, filterable list of subjects.
// GET /api/v2/subjects?page=1&page_size=50&search=john&status=active&expires_before=2026-12-31&expires_after=2026-01-01
func (d Deps) handleListSubjectsV2(w http.ResponseWriter, r *http.Request) {
	// Parse pagination
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

	// Parse filters
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	expiresBeforeStr := strings.TrimSpace(r.URL.Query().Get("expires_before"))
	expiresAfterStr := strings.TrimSpace(r.URL.Query().Get("expires_after"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))

	// Build query
	conditions := []string{"1=1"}
	args := []interface{}{}
	argIdx := 1

	if search != "" {
		conditions = append(conditions, "(name LIKE ? OR note LIKE ?)")
		searchPattern := "%" + search + "%"
		args = append(args, searchPattern, searchPattern)
		argIdx += 2
	}

	if statusFilter != "" {
		switch statusFilter {
		case "active":
			conditions = append(conditions, "disabled = 0 AND frozen = 0")
		case "disabled":
			conditions = append(conditions, "disabled = 1")
		case "frozen":
			conditions = append(conditions, "frozen = 1")
		case "expired":
			conditions = append(conditions, "expires_at IS NOT NULL AND expires_at <= ?")
			args = append(args, time.Now().Unix())
			argIdx++
		}
	}

	if expiresBeforeStr != "" {
		if t, err := time.Parse("2006-01-02", expiresBeforeStr); err == nil {
			conditions = append(conditions, "expires_at IS NOT NULL AND expires_at <= ?")
			args = append(args, t.Unix())
			argIdx++
		}
	}

	if expiresAfterStr != "" {
		if t, err := time.Parse("2006-01-02", expiresAfterStr); err == nil {
			conditions = append(conditions, "expires_at IS NOT NULL AND expires_at >= ?")
			args = append(args, t.Unix())
			argIdx++
		}
	}

	trafficMin := strings.TrimSpace(r.URL.Query().Get("traffic_min"))
	if trafficMin != "" {
		if parsed, err := strconv.ParseInt(trafficMin, 10, 64); err == nil && parsed >= 0 {
			conditions = append(conditions, "quota_used_bytes >= ?")
			args = append(args, parsed)
			argIdx++
		}
	}

	trafficMax := strings.TrimSpace(r.URL.Query().Get("traffic_max"))
	if trafficMax != "" {
		if parsed, err := strconv.ParseInt(trafficMax, 10, 64); err == nil && parsed >= 0 {
			conditions = append(conditions, "quota_used_bytes <= ?")
			args = append(args, parsed)
			argIdx++
		}
	}

	quotaStatus := strings.TrimSpace(r.URL.Query().Get("quota_status"))
	if quotaStatus == "under_limit" {
		conditions = append(conditions, "quota_bytes IS NOT NULL AND quota_used_bytes < quota_bytes")
	} else if quotaStatus == "near_limit" {
		conditions = append(conditions, "quota_bytes IS NOT NULL AND quota_used_bytes >= quota_bytes * 0.8 AND quota_used_bytes < quota_bytes")
	} else if quotaStatus == "over_limit" {
		conditions = append(conditions, "quota_bytes IS NOT NULL AND quota_used_bytes >= quota_bytes")
	}

	sortBy := strings.TrimSpace(r.URL.Query().Get("sort"))
	sortOrder := strings.TrimSpace(r.URL.Query().Get("order"))

	whereClause := strings.Join(conditions, " AND ")

	// Handle tag filter via note field
	if tag != "" {
		conditions = append(conditions, "note LIKE ?")
		args = append(args, "%"+tag+"%")
		whereClause = strings.Join(conditions, " AND ")
	}

	baseQuery := "SELECT id, name, note, disabled, frozen, expires_at, quota_bytes, quota_used_bytes, subscription_token, created_at, updated_at FROM subjects WHERE " + whereClause

	// Count total
	countQuery := "SELECT COUNT(*) FROM (" + baseQuery + ") AS filtered"
	var total int
	err := d.Store.Read().QueryRowContext(r.Context(), countQuery, args...).Scan(&total)
	if err != nil {
		http.Error(w, "failed to count subjects", http.StatusInternalServerError)
		return
	}

	// Fetch page with dynamic sorting
	orderClause := "ORDER BY created_at DESC"
	if sortBy != "" {
		validSorts := map[string]string{
			"name":    "name",
			"created": "created_at",
			"expires": "expires_at",
			"traffic": "quota_used_bytes",
			"quota":   "quota_bytes",
		}
		if col, ok := validSorts[sortBy]; ok {
			direction := "DESC"
			if sortOrder == "asc" {
				direction = "ASC"
			}
			orderClause = "ORDER BY " + col + " " + direction
		}
	}

	query := baseQuery + " " + orderClause + " LIMIT ? OFFSET ?"
	args = append(args, pageSize, offset)

	rows, err := d.Store.Read().QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to query subjects", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	subjects := []subjectDTO{}
	for rows.Next() {
		var id, createdAt int64
		var name string
		var enabled bool
		var expiresAt, expiredAt *int64
		var note string

		err := rows.Scan(&id, &name, &note, &enabled, &expiresAt, &createdAt)
		if err != nil {
			http.Error(w, "failed to scan subject", http.StatusInternalServerError)
			return
		}

		subjects = append(subjects, subjectDTO{
			ID:        id,
			Name:      name,
			Note:      note,
			Enabled:   enabled,
			ExpiresAt: expiresAt,
			ExpiredAt: expiredAt,
			CreatedAt: createdAt,
		})
	}

	if err := rows.Err(); err != nil {
		http.Error(w, "row iteration error", http.StatusInternalServerError)
		return
	}

	resp := SubjectListV2Response{
		Subjects: subjects,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
