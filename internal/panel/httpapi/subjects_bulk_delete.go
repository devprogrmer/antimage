package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/rbac"
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
	// Permission before scope; see handleBulkEnableSubjects. Deletion carries
	// no permission of its own -- subject:write covers it, matching the
	// single-subject path, which reaches the same gate through the service
	// layer's authorize().
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	var req BulkDeleteRequest
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

	deleted := 0
	failed := 0
	errMsgs := []string{}

	// Delegate to the service layer instead of issuing DELETEs here. It owns
	// the transaction, captures the affected nodes BEFORE the cascade removes
	// the grants that name them, and republishes each through CommitNodeChange.
	//
	// The inline version this replaces did neither correctly: it read node_id
	// from subject_services, a column that table does not have (node_id lives
	// on services), so every delete failed at SQL; and it collected the
	// affected nodes into a map it then dropped on the floor, so had the query
	// worked, a deleted subject would have stayed in every node's desired
	// document until something unrelated bumped the revision.
	//
	// One transaction per subject rather than one for the batch: this API
	// already reports per-subject success and failure, and one bad id must not
	// roll back the subjects that succeeded.
	svc := d.subjectService()
	sa := d.svcActor(r, actor)
	ctx := r.Context()
	for _, subjectID := range req.SubjectIDs {
		if err := svc.Delete(ctx, sa, subjectID); err != nil {
			errMsgs = append(errMsgs, err.Error())
			failed++
			continue
		}
		deleted++
	}

	resp := BulkDeleteResponse{
		Deleted: deleted,
		Failed:  failed,
		Errors:  errMsgs,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
