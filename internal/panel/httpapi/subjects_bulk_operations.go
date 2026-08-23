package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// BulkEnableRequest specifies subjects to enable.
type BulkEnableRequest struct {
	SubjectIDs []int64 `json:"subject_ids"`
}

// BulkEnableResponse reports enable results.
type BulkEnableResponse struct {
	Enabled int      `json:"enabled"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// handleBulkEnableSubjects enables multiple subjects.
// POST /api/v1/subjects/bulk/enable
func (d Deps) handleBulkEnableSubjects(w http.ResponseWriter, r *http.Request) {
	var req BulkEnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.SubjectIDs) == 0 {
		http.Error(w, "subject_ids required", http.StatusBadRequest)
		return
	}

	if len(req.SubjectIDs) > 1000 {
		http.Error(w, "maximum 1000 subjects per request", http.StatusBadRequest)
		return
	}
	// Drop ids outside this caller's tenant. Filtering rather than rejecting
	// keeps an out-of-scope id indistinguishable from a nonexistent one.
	scoped, scopeErr := d.scopeFilterSubjectIDs(r, req.SubjectIDs)
	if scopeErr != nil {
		http.Error(w, "could not check subject scope", http.StatusInternalServerError)
		return
	}
	req.SubjectIDs = scoped

	enabled := 0
	failed := 0
	errors := []string{}

	err := d.Store.Write(r.Context(), func(tx *sql.Tx) error {
		for _, subjectID := range req.SubjectIDs {
			result, err := tx.ExecContext(r.Context(), `
				UPDATE subjects SET disabled = 0, updated_at = ? WHERE id = ?
			`, time.Now().Unix(), subjectID)
			if err != nil {
				errors = append(errors, err.Error())
				failed++
				continue
			}

			rowsAffected, _ := result.RowsAffected()
			if rowsAffected == 0 {
				errors = append(errors, "subject not found")
				failed++
				continue
			}

			enabled++
		}
		return nil
	})

	if err != nil {
		http.Error(w, "transaction failed", http.StatusInternalServerError)
		return
	}

	resp := BulkEnableResponse{
		Enabled: enabled,
		Failed:  failed,
		Errors:  errors,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// BulkExtendRequest specifies subjects and extension period.
type BulkExtendRequest struct {
	SubjectIDs []int64 `json:"subject_ids"`
	Days       int     `json:"days"` // Number of days to extend
}

// BulkExtendResponse reports extension results.
type BulkExtendResponse struct {
	Extended int      `json:"extended"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
}

// handleBulkExtendSubjects extends expiry for multiple subjects.
// POST /api/v1/subjects/bulk/extend
func (d Deps) handleBulkExtendSubjects(w http.ResponseWriter, r *http.Request) {
	var req BulkExtendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.SubjectIDs) == 0 {
		http.Error(w, "subject_ids required", http.StatusBadRequest)
		return
	}

	if req.Days <= 0 || req.Days > 3650 {
		http.Error(w, "days must be between 1 and 3650", http.StatusBadRequest)
		return
	}

	if len(req.SubjectIDs) > 1000 {
		http.Error(w, "maximum 1000 subjects per request", http.StatusBadRequest)
		return
	}
	// Drop ids outside this caller's tenant. Filtering rather than rejecting
	// keeps an out-of-scope id indistinguishable from a nonexistent one.
	scoped, scopeErr := d.scopeFilterSubjectIDs(r, req.SubjectIDs)
	if scopeErr != nil {
		http.Error(w, "could not check subject scope", http.StatusInternalServerError)
		return
	}
	req.SubjectIDs = scoped

	extended := 0
	failed := 0
	errors := []string{}

	err := d.Store.Write(r.Context(), func(tx *sql.Tx) error {
		for _, subjectID := range req.SubjectIDs {
			// Get current expiry
			var currentExpiry sql.NullInt64
			err := tx.QueryRowContext(r.Context(), `
				SELECT expires_at FROM subjects WHERE id = ?
			`, subjectID).Scan(&currentExpiry)
			if err != nil {
				if err == sql.ErrNoRows {
					errors = append(errors, "subject not found")
				} else {
					errors = append(errors, err.Error())
				}
				failed++
				continue
			}

			// Calculate new expiry
			var newExpiry int64
			if currentExpiry.Valid {
				// Extend from current expiry if set
				newExpiry = currentExpiry.Int64 + int64(req.Days*24*3600)
			} else {
				// Set expiry from now if not set
				newExpiry = time.Now().Unix() + int64(req.Days*24*3600)
			}

			// Update expiry
			result, err := tx.ExecContext(r.Context(), `
				UPDATE subjects SET expires_at = ?, updated_at = ? WHERE id = ?
			`, newExpiry, time.Now().Unix(), subjectID)
			if err != nil {
				errors = append(errors, err.Error())
				failed++
				continue
			}

			rowsAffected, _ := result.RowsAffected()
			if rowsAffected == 0 {
				errors = append(errors, "update failed")
				failed++
				continue
			}

			extended++
		}
		return nil
	})

	if err != nil {
		http.Error(w, "transaction failed", http.StatusInternalServerError)
		return
	}

	resp := BulkExtendResponse{
		Extended: extended,
		Failed:   failed,
		Errors:   errors,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// BulkResetTrafficRequest specifies subjects to reset.
type BulkResetTrafficRequest struct {
	SubjectIDs []int64 `json:"subject_ids"`
}

// BulkResetTrafficResponse reports reset results.
type BulkResetTrafficResponse struct {
	Reset  int      `json:"reset"`
	Failed int      `json:"failed"`
	Errors []string `json:"errors,omitempty"`
}

// handleBulkResetTraffic resets traffic counters for multiple subjects.
// POST /api/v1/subjects/bulk/reset-traffic
func (d Deps) handleBulkResetTraffic(w http.ResponseWriter, r *http.Request) {
	var req BulkResetTrafficRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.SubjectIDs) == 0 {
		http.Error(w, "subject_ids required", http.StatusBadRequest)
		return
	}

	if len(req.SubjectIDs) > 1000 {
		http.Error(w, "maximum 1000 subjects per request", http.StatusBadRequest)
		return
	}
	// Drop ids outside this caller's tenant. Filtering rather than rejecting
	// keeps an out-of-scope id indistinguishable from a nonexistent one.
	scoped, scopeErr := d.scopeFilterSubjectIDs(r, req.SubjectIDs)
	if scopeErr != nil {
		http.Error(w, "could not check subject scope", http.StatusInternalServerError)
		return
	}
	req.SubjectIDs = scoped

	reset := 0
	failed := 0
	errors := []string{}

	err := d.Store.Write(r.Context(), func(tx *sql.Tx) error {
		for _, subjectID := range req.SubjectIDs {
			result, err := tx.ExecContext(r.Context(), `
				UPDATE subjects SET quota_used_bytes = 0, updated_at = ? WHERE id = ?
			`, time.Now().Unix(), subjectID)
			if err != nil {
				errors = append(errors, err.Error())
				failed++
				continue
			}

			rowsAffected, _ := result.RowsAffected()
			if rowsAffected == 0 {
				errors = append(errors, "subject not found")
				failed++
				continue
			}

			reset++
		}
		return nil
	})

	if err != nil {
		http.Error(w, "transaction failed", http.StatusInternalServerError)
		return
	}

	resp := BulkResetTrafficResponse{
		Reset:  reset,
		Failed: failed,
		Errors: errors,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// BulkSetQuotaRequest specifies subjects and new quota.
type BulkSetQuotaRequest struct {
	SubjectIDs []int64 `json:"subject_ids"`
	QuotaBytes int64   `json:"quota_bytes"` // 0 = unlimited
}

// BulkSetQuotaResponse reports quota update results.
type BulkSetQuotaResponse struct {
	Updated int      `json:"updated"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// handleBulkSetQuota updates quota for multiple subjects.
// POST /api/v1/subjects/bulk/set-quota
func (d Deps) handleBulkSetQuota(w http.ResponseWriter, r *http.Request) {
	var req BulkSetQuotaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.SubjectIDs) == 0 {
		http.Error(w, "subject_ids required", http.StatusBadRequest)
		return
	}

	if req.QuotaBytes < 0 {
		http.Error(w, "quota_bytes must be >= 0", http.StatusBadRequest)
		return
	}

	if len(req.SubjectIDs) > 1000 {
		http.Error(w, "maximum 1000 subjects per request", http.StatusBadRequest)
		return
	}
	// Drop ids outside this caller's tenant. Filtering rather than rejecting
	// keeps an out-of-scope id indistinguishable from a nonexistent one.
	scoped, scopeErr := d.scopeFilterSubjectIDs(r, req.SubjectIDs)
	if scopeErr != nil {
		http.Error(w, "could not check subject scope", http.StatusInternalServerError)
		return
	}
	req.SubjectIDs = scoped

	updated := 0
	failed := 0
	errors := []string{}

	err := d.Store.Write(r.Context(), func(tx *sql.Tx) error {
		for _, subjectID := range req.SubjectIDs {
			var quotaValue sql.NullInt64
			if req.QuotaBytes > 0 {
				quotaValue = sql.NullInt64{Int64: req.QuotaBytes, Valid: true}
			}

			result, err := tx.ExecContext(r.Context(), `
				UPDATE subjects SET quota_bytes = ?, updated_at = ? WHERE id = ?
			`, quotaValue, time.Now().Unix(), subjectID)
			if err != nil {
				errors = append(errors, err.Error())
				failed++
				continue
			}

			rowsAffected, _ := result.RowsAffected()
			if rowsAffected == 0 {
				errors = append(errors, "subject not found")
				failed++
				continue
			}

			updated++
		}
		return nil
	})

	if err != nil {
		http.Error(w, "transaction failed", http.StatusInternalServerError)
		return
	}

	resp := BulkSetQuotaResponse{
		Updated: updated,
		Failed:  failed,
		Errors:  errors,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
