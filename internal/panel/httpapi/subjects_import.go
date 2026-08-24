package httpapi

import (
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/resellers"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// ImportSubjectsRequest is the payload for CSV import.
type ImportSubjectsRequest struct {
	CSV string `json:"csv"` // CSV content as string

	// ResellerID assigns ownership of every imported row. Without it the rows
	// are platform-owned, which is what import did unconditionally before:
	// they belonged to nobody and were invisible to every tenant.
	//
	// Only an actor holding reseller:write may set it. Assigning ownership
	// decides who is billed, so it is deliberately not something a tenant can
	// do for themselves -- a reseller who could name their own id here would
	// be provisioning on their own account at a cost they also chose.
	ResellerID *int64 `json:"reseller_id,omitempty"`

	// Cost is charged to the reseller per imported row. Zero records ownership
	// without touching the ledger, which is how an operator migrates existing
	// customers to a reseller without billing them for the migration.
	Cost int64 `json:"cost,omitempty"`

	// ServiceIDs are granted to every imported subject. Without at least one, a
	// subject appears on no node: the desired document is built by joining
	// through subject_services.
	ServiceIDs []int64 `json:"service_ids,omitempty"`
}

// ImportSubjectsResponse reports import results.
type ImportSubjectsResponse struct {
	Imported int      `json:"imported"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
}

// importKey derives a stable idempotency key for one row of one CSV.
//
// ProvisionSubject requires a key and treats a repeat as a replay, returning
// the original outcome instead of charging again. Deriving it from the CSV
// content plus the subject name makes re-POSTing an identical import safe:
// rows that already landed are recognised rather than duplicated or
// double-billed. A different CSV is a different batch and gets different keys.
func importKey(csvBody, name string) string {
	sum := sha256.Sum256([]byte(csvBody + "\x00" + name))
	return "import:" + hex.EncodeToString(sum[:12])
}

// handleImportSubjects imports subjects from CSV.
// POST /api/v1/subjects/import
// Body: {"csv": "Name,Note,Disabled,Frozen,ExpiresAt,...", "reseller_id": 3}
func (d Deps) handleImportSubjects(w http.ResponseWriter, r *http.Request) {
	// Import had no permission check at all. That was survivable only because
	// it also failed at SQL on every row; repairing the columns without this
	// gate would have handed subject creation to any authenticated caller,
	// including a readonly one. Same reasoning as the bulk endpoints.
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSubjectWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	var req ImportSubjectsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Naming an owner is a billing decision, so it needs the tenancy
	// permission on top of subject:write. subject:write alone lets you create
	// customers; it does not let you decide whose account they land on.
	if req.ResellerID != nil {
		if !d.authorize(w, r, actor, rbac.PermResellerWrite, rbac.Target{Kind: rbac.TargetNone}) {
			return
		}
	}

	csvReader := csv.NewReader(strings.NewReader(req.CSV))
	records, err := csvReader.ReadAll()
	if err != nil {
		http.Error(w, "invalid CSV format", http.StatusBadRequest)
		return
	}

	if len(records) < 2 {
		http.Error(w, "CSV must have header and at least one row", http.StatusBadRequest)
		return
	}

	// Parse header to column index map
	header := records[0]
	colMap := make(map[string]int)
	for i, col := range header {
		colMap[col] = i
	}

	imported := 0
	failed := 0
	errMsgs := []string{}

	svc := d.subjectService()
	sa := d.svcActor(r, actor)
	ctx := r.Context()

	// Process each row
	for _, row := range records[1:] {
		if len(row) < 1 {
			errMsgs = append(errMsgs, "insufficient columns")
			failed++
			continue
		}

		idx, ok := colMap["Name"]
		if !ok || idx >= len(row) || row[idx] == "" {
			errMsgs = append(errMsgs, "name required")
			failed++
			continue
		}
		name := row[idx]

		// note is TEXT NOT NULL DEFAULT '', so an absent column is the empty
		// string, not NULL. Binding sql.NullString{} here failed the NOT NULL
		// constraint for every row whose Note was blank.
		note := ""
		if i, ok := colMap["Note"]; ok && i < len(row) {
			note = row[i]
		}

		var expiresAt *time.Time
		if i, ok := colMap["ExpiresAt"]; ok && i < len(row) && row[i] != "" {
			if t, err := time.Parse(time.RFC3339, row[i]); err == nil {
				expiresAt = &t
			}
		}

		var quotaBytes int64
		if i, ok := colMap["QuotaBytes"]; ok && i < len(row) && row[i] != "" {
			if qb, err := strconv.ParseInt(row[i], 10, 64); err == nil && qb > 0 {
				quotaBytes = qb
			}
		}

		// The CSV keeps the Disabled and Frozen columns the export writes, but
		// the table stores neither: `enabled` carries the inverted sense and
		// `frozen_at` is a nullable timestamp. Both are applied after creation
		// through the service, which republishes -- a subject's enabled state
		// decides whether it appears in a node's desired document.
		disabled := false
		if i, ok := colMap["Disabled"]; ok && i < len(row) {
			disabled, _ = strconv.ParseBool(row[i])
		}
		frozen := false
		if i, ok := colMap["Frozen"]; ok && i < len(row) {
			frozen, _ = strconv.ParseBool(row[i])
		}

		// Created through the ordinary subject path, not a raw INSERT. The
		// INSERT this replaces minted no credentials and granted no services,
		// so every imported subject was inert: it could not authenticate and
		// appeared on no node. Create seals both credential kinds under the
		// master key and writes the service grants in the same transaction.
		//
		// SubscriptionToken is deliberately not imported. Tokens are issued
		// lazily by subjects/tokens.go and carry a UNIQUE index, so replaying
		// an exported one would either collide or clone a live credential onto
		// a second subject.
		in := subjects.CreateInput{
			Name:       name,
			Note:       note,
			ExpiresAt:  expiresAt,
			ServiceIDs: req.ServiceIDs,
		}

		var id int64
		if req.ResellerID != nil {
			// Provision records ownership, checks the reseller's ceilings and
			// credit floor, and debits the ledger, all in the transaction that
			// creates the subject.
			out, perr := svc.Provision(ctx, sa, resellers.ProvisionInput{
				ResellerID:     *req.ResellerID,
				Cost:           req.Cost,
				Subject:        in,
				QuotaBytes:     quotaBytes,
				IdempotencyKey: importKey(req.CSV, name),
			})
			if perr != nil {
				errMsgs = append(errMsgs, perr.Error())
				failed++
				continue
			}
			id = out.SubjectID
		} else {
			var cerr error
			id, cerr = svc.Create(ctx, sa, in)
			if cerr != nil {
				errMsgs = append(errMsgs, cerr.Error())
				failed++
				continue
			}
		}

		// Quota is not carried by CreateInput or ProvisionInput -- the latter
		// only measures it against the reseller's ceiling. It is not part of
		// the desired document either, so setting it needs no republish.
		if quotaBytes > 0 {
			if err := d.Store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx,
					`UPDATE subjects SET quota_bytes = ? WHERE id = ?`, quotaBytes, id)
				return err
			}); err != nil {
				errMsgs = append(errMsgs, "subject "+name+": set quota: "+err.Error())
				failed++
				continue
			}
		}

		if disabled {
			if err := svc.SetEnabled(ctx, sa, id, false); err != nil {
				errMsgs = append(errMsgs, "subject "+name+": disable: "+err.Error())
				failed++
				continue
			}
		}
		if frozen {
			if err := svc.SetFrozen(ctx, sa, id, true, "imported frozen"); err != nil {
				errMsgs = append(errMsgs, "subject "+name+": freeze: "+err.Error())
				failed++
				continue
			}
		}

		imported++
	}

	resp := ImportSubjectsResponse{
		Imported: imported,
		Failed:   failed,
		Errors:   errMsgs,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
