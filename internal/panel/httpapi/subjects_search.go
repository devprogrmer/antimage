package httpapi

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SubjectListV2Response includes pagination metadata.
type SubjectListV2Response struct {
	Subjects []Subject `json:"subjects"`
	Total    int       `json:"total"`
	Page     int       `json:"page"`
	PageSize int       `json:"page_size"`
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

	whereClause := strings.Join(conditions, " AND ")

	// Handle tag filter (if tag system exists)
	var baseQuery string
	if tag != "" {
		baseQuery = `
			SELECT DISTINCT s.* FROM subjects s
			INNER JOIN subject_tags st ON s.id = st.subject_id
			WHERE st.tag = ? AND ` + whereClause
		args = append([]interface{}{tag}, args...)
	} else {
		baseQuery = "SELECT * FROM subjects WHERE " + whereClause
	}

	// Count total
	countQuery := "SELECT COUNT(*) FROM (" + baseQuery + ") AS filtered"
	var total int
	err := d.Store.Read().QueryRowContext(r.Context(), countQuery, args...).Scan(&total)
	if err != nil {
		http.Error(w, "failed to count subjects", http.StatusInternalServerError)
		return
	}

	// Fetch page
	query := baseQuery + " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, pageSize, offset)

	rows, err := d.Store.Read().QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to query subjects", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	subjects := []Subject{}
	for rows.Next() {
		var s Subject
		err := rows.Scan(
			&s.ID,
			&s.Name,
			&s.Note,
			&s.Disabled,
			&s.Frozen,
			&s.ExpiresAt,
			&s.QuotaBytes,
			&s.QuotaUsedBytes,
			&s.SubscriptionToken,
			&s.CreatedAt,
			&s.UpdatedAt,
		)
		if err != nil {
			http.Error(w, "failed to scan subject", http.StatusInternalServerError)
			return
		}
		subjects = append(subjects, s)
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
