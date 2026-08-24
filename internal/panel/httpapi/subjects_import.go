package httpapi

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
)

// ImportSubjectsRequest is the payload for CSV import.
type ImportSubjectsRequest struct {
	CSV string `json:"csv"` // CSV content as string
}

// ImportSubjectsResponse reports import results.
type ImportSubjectsResponse struct {
	Imported int      `json:"imported"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
}

// handleImportSubjects imports subjects from CSV.
// POST /api/v1/subjects/import
// Body: {"csv": "Name,Note,Disabled,Frozen,ExpiresAt,..."}
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
	errors := []string{}

	// Process each row
	for _, row := range records[1:] {
		if len(row) < 2 {
			errors = append(errors, "insufficient columns")
			failed++
			continue
		}

		// Parse row
		name := row[colMap["Name"]]
		if name == "" {
			errors = append(errors, "name required")
			failed++
			continue
		}

		// The CSV keeps the Disabled and Frozen columns the export writes, but
		// the table stores neither: `enabled` carries the inverted sense and
		// `frozen_at` is a nullable timestamp. Both are translated here rather
		// than changing a format an operator may already have on disk.
		enabled := true
		if idx, ok := colMap["Disabled"]; ok && idx < len(row) {
			if disabled, err := strconv.ParseBool(row[idx]); err == nil {
				enabled = !disabled
			}
		}

		var frozenAt sql.NullInt64
		if idx, ok := colMap["Frozen"]; ok && idx < len(row) {
			if frozen, err := strconv.ParseBool(row[idx]); err == nil && frozen {
				frozenAt = sql.NullInt64{Valid: true, Int64: d.now().Unix()}
			}
		}

		var expiresAt sql.NullInt64
		if idx, ok := colMap["ExpiresAt"]; ok && idx < len(row) && row[idx] != "" {
			if t, err := time.Parse(time.RFC3339, row[idx]); err == nil {
				expiresAt = sql.NullInt64{Valid: true, Int64: t.Unix()}
			}
		}

		var quotaBytes sql.NullInt64
		if idx, ok := colMap["QuotaBytes"]; ok && idx < len(row) && row[idx] != "" {
			if qb, err := strconv.ParseInt(row[idx], 10, 64); err == nil {
				quotaBytes = sql.NullInt64{Valid: true, Int64: qb}
			}
		}

		// note is TEXT NOT NULL DEFAULT '', so an absent column is the empty
		// string, not NULL. Binding sql.NullString{} here failed the NOT NULL
		// constraint for every row whose Note was blank.
		note := ""
		if idx, ok := colMap["Note"]; ok && idx < len(row) {
			note = row[idx]
		}

		// Generate subscription token
		tokenBytes := make([]byte, 16)
		if _, err := rand.Read(tokenBytes); err != nil {
			errors = append(errors, "token generation failed")
			failed++
			continue
		}
		subscriptionToken := base64.URLEncoding.EncodeToString(tokenBytes)

		// Insert subject. No updated_at column exists, and frozen_at doubles as
		// the frozen flag.
		now := d.now().Unix()
		ctx := r.Context()
		err := d.Store.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO subjects (name, note, enabled, frozen_at, expires_at, quota_bytes, quota_used_bytes, subscription_token, created_at)
				VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)
			`, name, note, enabled, frozenAt, expiresAt, quotaBytes, subscriptionToken, now)
			return err
		})

		if err != nil {
			errors = append(errors, err.Error())
			failed++
			continue
		}

		imported++
	}

	resp := ImportSubjectsResponse{
		Imported: imported,
		Failed:   failed,
		Errors:   errors,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
