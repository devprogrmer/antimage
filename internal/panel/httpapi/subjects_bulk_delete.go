package httpapi

import (
	"database/sql"
	"net/http"
)

// BulkDeleteRequest specifies subjects to delete.
type BulkDeleteRequest struct {
	SubjectIDs []int64 `json:"subject_ids"`
}

// BulkDeleteResponse reports deletion results.
type BulkDeleteResponse struct {
	Deleted int      `json:"deleted"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// handleBulkDeleteSubjects deletes multiple subjects.
// POST /api/v1/subjects/bulk/delete
func (d Deps) handleBulkDeleteSubjects(w http.ResponseWriter, r *http.Request) {
	var req BulkDeleteRequest
	if err := decodeJSON(r.Body, &req); err != nil {
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

	deleted := 0
	failed := 0
	errors := []string{}
	affectedNodes := make(map[int64]struct{})

	tx, err := d.Store.Write().BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "failed to begin transaction", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	for _, subjectID := range req.SubjectIDs {
		// Check if subject exists and collect affected nodes
		rows, err := tx.QueryContext(r.Context(), `
			SELECT node_id FROM subject_services WHERE subject_id = ?
		`, subjectID)
		if err != nil {
			errors = append(errors, err.Error())
			failed++
			continue
		}

		for rows.Next() {
			var nodeID int64
			if err := rows.Scan(&nodeID); err == nil {
				affectedNodes[nodeID] = struct{}{}
			}
		}
		rows.Close()

		// Delete subject
		result, err := tx.ExecContext(r.Context(), `DELETE FROM subjects WHERE id = ?`, subjectID)
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

		deleted++
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "failed to commit transaction", http.StatusInternalServerError)
		return
	}

	// Republish affected nodes
	for nodeID := range affectedNodes {
		if err := d.Hub.Republish(r.Context(), nodeID); err != nil {
			// Log but don't fail the bulk operation
			_ = err
		}
	}

	resp := BulkDeleteResponse{
		Deleted: deleted,
		Failed:  failed,
		Errors:  errors,
	}

	writeJSON(w, http.StatusOK, resp)
}
