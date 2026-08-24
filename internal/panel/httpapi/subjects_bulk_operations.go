package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/subjects"
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
	// Permission first, then scope -- the two answer different questions and
	// neither substitutes for the other. The scope filter decides WHICH
	// subjects this caller may touch; it silently drops the rest, so a role
	// carrying no subject:write but a non-empty scope would still mutate its
	// own subjects unchallenged. Only rbac.Check decides whether the caller may
	// perform the operation at all. See docs/TENANT-ISOLATION.md.
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

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
	errMsgs := []string{}

	// Through the service layer: enabling changes which subjects appear in a
	// node's desired document (buildSubjects filters on s.enabled = 1), so it
	// must republish. The UPDATE this replaces wrote a `disabled` column that
	// does not exist -- the real column is `enabled`, with inverted sense --
	// and set an `updated_at` that does not exist either, so it failed at SQL
	// on every row and never republished anything.
	svc := d.subjectService()
	sa := d.svcActor(r, actor)
	ctx := r.Context()
	for _, subjectID := range req.SubjectIDs {
		if err := svc.SetEnabled(ctx, sa, subjectID, true); err != nil {
			errMsgs = append(errMsgs, err.Error())
			failed++
			continue
		}
		enabled++
	}

	resp := BulkEnableResponse{
		Enabled: enabled,
		Failed:  failed,
		Errors:  errMsgs,
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
	// Permission before scope; see handleBulkEnableSubjects.
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

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
	errMsgs := []string{}

	// Extending expiry can move a subject back into a node's desired document,
	// since buildSubjects filters on expires_at > now, so this republishes too
	// and therefore goes through the service layer. The previous UPDATE also
	// wrote a nonexistent updated_at column and failed at SQL for every row.
	svc := d.subjectService()
	sa := d.svcActor(r, actor)
	ctx := r.Context()
	extendBy := time.Duration(req.Days) * 24 * time.Hour

	for _, subjectID := range req.SubjectIDs {
		// Extend from the current expiry when one is set, otherwise from now --
		// so extending an already-expired subject does not silently backdate the
		// new expiry into the past and leave it excluded from every node.
		var current sql.NullInt64
		err := d.Store.Read().QueryRowContext(ctx,
			`SELECT expires_at FROM subjects WHERE id = ?`, subjectID).Scan(&current)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				errMsgs = append(errMsgs, "subject not found")
			} else {
				errMsgs = append(errMsgs, err.Error())
			}
			failed++
			continue
		}

		base := d.now()
		if current.Valid && current.Int64 > base.Unix() {
			base = time.Unix(current.Int64, 0)
		}
		newExpiry := base.Add(extendBy)

		if err := svc.Update(ctx, sa, subjectID, subjects.UpdateInput{
			ExpiresAt: &newExpiry,
		}); err != nil {
			errMsgs = append(errMsgs, err.Error())
			failed++
			continue
		}
		extended++
	}

	resp := BulkExtendResponse{
		Extended: extended,
		Failed:   failed,
		Errors:   errMsgs,
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
	// Permission before scope; see handleBulkEnableSubjects.
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

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
	errMsgs := []string{}

	// Usage counters stay in one transaction and do NOT republish: quota is not
	// part of the desired document (see nodes/document.go Subject), so zeroing
	// quota_used_bytes changes nothing a node would observe. Only the dropped
	// updated_at column was wrong here.
	//
	// Freezing is deliberately left alone. A subject frozen for quota is
	// unfrozen by the quota sweeper when its period rolls over, and that path
	// does republish because it flips enabled; silently unfreezing here would
	// duplicate that decision in a second place.
	ctx := r.Context()
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, subjectID := range req.SubjectIDs {
			result, err := tx.ExecContext(ctx, `
				UPDATE subjects SET quota_used_bytes = 0 WHERE id = ?
			`, subjectID)
			if err != nil {
				errMsgs = append(errMsgs, err.Error())
				failed++
				continue
			}

			rowsAffected, _ := result.RowsAffected()
			if rowsAffected == 0 {
				errMsgs = append(errMsgs, "subject not found")
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
		Errors: errMsgs,
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
	// Permission before scope; see handleBulkEnableSubjects.
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

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
	errMsgs := []string{}

	ctx := r.Context()
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		for _, subjectID := range req.SubjectIDs {
			var quotaValue sql.NullInt64
			if req.QuotaBytes > 0 {
				quotaValue = sql.NullInt64{Int64: req.QuotaBytes, Valid: true}
			}

			// No republish: quota_bytes is not part of the desired document.
			// Only the dropped updated_at column was wrong here.
			result, err := tx.ExecContext(ctx, `
				UPDATE subjects SET quota_bytes = ? WHERE id = ?
			`, quotaValue, subjectID)
			if err != nil {
				errMsgs = append(errMsgs, err.Error())
				failed++
				continue
			}

			rowsAffected, _ := result.RowsAffected()
			if rowsAffected == 0 {
				errMsgs = append(errMsgs, "subject not found")
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
		Errors:  errMsgs,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
