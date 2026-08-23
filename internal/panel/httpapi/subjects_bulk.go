package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// bulkCreateRequest contains parameters for bulk subject creation.
type bulkCreateRequest struct {
	Subjects []struct {
		Name        string            `json:"name"`
		Note        string            `json:"note"`
		ExpiresAt   *int64            `json:"expires_at"`
		ServiceIDs  []int64           `json:"service_ids"`
		Credentials map[string]string `json:"credentials"`
	} `json:"subjects"`
}

type bulkCreateResponse struct {
	Created []struct {
		Name string `json:"name"`
		ID   int64  `json:"id"`
	} `json:"created"`
	Failed []struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	} `json:"failed"`
}

// handleBulkCreateSubjects creates multiple subjects in one transaction.
func (d Deps) handleBulkCreateSubjects(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	var req bulkCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	if len(req.Subjects) == 0 {
		WriteError(w, http.StatusBadRequest, "bad_request", "no subjects provided")
		return
	}

	if len(req.Subjects) > 1000 {
		WriteError(w, http.StatusBadRequest, "bad_request", "bulk operation limited to 1000 subjects")
		return
	}

	ctx := r.Context()
	store := d.subjectStore()

	var resp bulkCreateResponse
	resp.Created = []struct {
		Name string `json:"name"`
		ID   int64  `json:"id"`
	}{}
	resp.Failed = []struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}{}

	// Process each subject
	for _, subj := range req.Subjects {
		in := subjects.CreateInput{
			Name:        subj.Name,
			Note:        subj.Note,
			ServiceIDs:  subj.ServiceIDs,
			Credentials: map[subjects.CredentialKind]string{},
		}

		if subj.ExpiresAt != nil {
			t := time.Unix(*subj.ExpiresAt, 0).UTC()
			in.ExpiresAt = &t
		}

		for kind, value := range subj.Credentials {
			k := subjects.CredentialKind(kind)
			if err := subjects.ValidateCredential(k, value); err != nil {
				resp.Failed = append(resp.Failed, struct {
					Name   string `json:"name"`
					Reason string `json:"reason"`
				}{Name: subj.Name, Reason: err.Error()})
				continue
			}
			in.Credentials[k] = value
		}

		var subjectID int64
		err := d.Store.Write(ctx, func(tx *sql.Tx) error {
			id, err := store.Create(ctx, tx, in)
			if err != nil {
				return err
			}
			subjectID = id
			return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
				Action:     "subject.bulk_create",
				TargetType: "subject",
				TargetID:   sql.NullInt64{Int64: id, Valid: true},
				After:      map[string]any{"name": in.Name},
				Result:     "ok",
			})
		})

		if err != nil {
			resp.Failed = append(resp.Failed, struct {
				Name   string `json:"name"`
				Reason string `json:"reason"`
			}{Name: subj.Name, Reason: err.Error()})
			continue
		}

		// Republish to nodes
		if err := d.republishSubject(ctx, r, actor, subjectID, "bulk created"); err != nil {
			resp.Failed = append(resp.Failed, struct {
				Name   string `json:"name"`
				Reason string `json:"reason"`
			}{Name: subj.Name, Reason: "created but republish failed"})
			continue
		}

		resp.Created = append(resp.Created, struct {
			Name string `json:"name"`
			ID   int64  `json:"id"`
		}{Name: subj.Name, ID: subjectID})
	}

	WriteJSON(w, http.StatusOK, resp)
}

// bulkUpdateRequest contains parameters for bulk subject updates.
type bulkUpdateRequest struct {
	SubjectIDs []int64 `json:"subject_ids"`
	Updates    struct {
		Enabled   *bool   `json:"enabled"`
		Note      *string `json:"note"`
		ExpiresAt *int64  `json:"expires_at"`
	} `json:"updates"`
}

type bulkUpdateResponse struct {
	Updated []int64 `json:"updated"`
	Failed  []struct {
		ID     int64  `json:"id"`
		Reason string `json:"reason"`
	} `json:"failed"`
}

// handleBulkUpdateSubjects updates multiple subjects with the same changes.
func (d Deps) handleBulkUpdateSubjects(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	var req bulkUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	if len(req.SubjectIDs) == 0 {
		WriteError(w, http.StatusBadRequest, "bad_request", "no subject IDs provided")
		return
	}

	if len(req.SubjectIDs) > 1000 {
		WriteError(w, http.StatusBadRequest, "bad_request", "bulk operation limited to 1000 subjects")
		return
	}

	ctx := r.Context()
	store := d.subjectStore()

	var resp bulkUpdateResponse
	resp.Updated = []int64{}
	resp.Failed = []struct {
		ID     int64  `json:"id"`
		Reason string `json:"reason"`
	}{}

	// Collect all affected nodes before updates
	allNodeIDs := make(map[int64]struct{})

	for _, id := range req.SubjectIDs {
		before, err := store.NodeIDsForRead(ctx, id)
		if err != nil {
			resp.Failed = append(resp.Failed, struct {
				ID     int64  `json:"id"`
				Reason string `json:"reason"`
			}{ID: id, Reason: "could not read subject"})
			continue
		}

		for _, nid := range before {
			allNodeIDs[nid] = struct{}{}
		}

		// Apply update
		up := subjects.UpdateInput{
			Enabled: req.Updates.Enabled,
			Note:    req.Updates.Note,
		}
		if req.Updates.ExpiresAt != nil {
			t := time.Unix(*req.Updates.ExpiresAt, 0).UTC()
			up.ExpiresAt = &t
		}

		err = d.Store.Write(ctx, func(tx *sql.Tx) error {
			if err := store.Update(ctx, tx, id, up); err != nil {
				return err
			}
			return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
				Action:     "subject.bulk_update",
				TargetType: "subject",
				TargetID:   sql.NullInt64{Int64: id, Valid: true},
				After:      map[string]any{"enabled": req.Updates.Enabled},
				Result:     "ok",
			})
		})

		if err != nil {
			resp.Failed = append(resp.Failed, struct {
				ID     int64  `json:"id"`
				Reason string `json:"reason"`
			}{ID: id, Reason: err.Error()})
			continue
		}

		resp.Updated = append(resp.Updated, id)
	}

	// Republish all affected nodes once
	nodeIDs := make([]int64, 0, len(allNodeIDs))
	for nid := range allNodeIDs {
		nodeIDs = append(nodeIDs, nid)
	}

	if err := d.republishNodes(ctx, r, actor, nodeIDs, "bulk update"); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal",
			"subjects updated but republish failed")
		return
	}

	WriteJSON(w, http.StatusOK, resp)
}

// bulkDisableRequest contains subject IDs to disable.
type bulkDisableRequest struct {
	SubjectIDs []int64 `json:"subject_ids"`
}

type bulkDisableResponse struct {
	Disabled []int64 `json:"disabled"`
	Failed   []struct {
		ID     int64  `json:"id"`
		Reason string `json:"reason"`
	} `json:"failed"`
}

// handleBulkDisableSubjects disables multiple subjects.
func (d Deps) handleBulkDisableSubjects(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	var req bulkDisableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	if len(req.SubjectIDs) > 1000 {
		WriteError(w, http.StatusBadRequest, "bad_request", "bulk operation limited to 1000 subjects")
		return
	}

	ctx := r.Context()
	store := d.subjectStore()

	var resp bulkDisableResponse
	resp.Disabled = []int64{}
	resp.Failed = []struct {
		ID     int64  `json:"id"`
		Reason string `json:"reason"`
	}{}

	allNodeIDs := make(map[int64]struct{})

	for _, id := range req.SubjectIDs {
		before, _ := store.NodeIDsForRead(ctx, id)
		for _, nid := range before {
			allNodeIDs[nid] = struct{}{}
		}

		enabled := false
		err := d.Store.Write(ctx, func(tx *sql.Tx) error {
			return store.Update(ctx, tx, id, subjects.UpdateInput{Enabled: &enabled})
		})

		if err != nil {
			resp.Failed = append(resp.Failed, struct {
				ID     int64  `json:"id"`
				Reason string `json:"reason"`
			}{ID: id, Reason: err.Error()})
			continue
		}

		resp.Disabled = append(resp.Disabled, id)
	}

	// Republish affected nodes
	nodeIDs := make([]int64, 0, len(allNodeIDs))
	for nid := range allNodeIDs {
		nodeIDs = append(nodeIDs, nid)
	}

	d.republishNodes(ctx, r, actor, nodeIDs, "bulk disable")

	WriteJSON(w, http.StatusOK, resp)
}
