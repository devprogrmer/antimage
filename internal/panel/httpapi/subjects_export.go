package httpapi

import (
	"database/sql"
	"encoding/csv"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// handleExportSubjects exports all subjects to CSV.
// GET /api/v1/subjects/export
func (d Deps) handleExportSubjects(w http.ResponseWriter, r *http.Request) {
	// Export is the highest-volume disclosure surface in the panel: one request
	// returns every column of every row, including subscription_token, which by
	// itself grants access to a user's configuration. Unscoped, a single GET by
	// any reseller dumped the entire customer base of every other tenant.
	if !d.requirePermission(w, r, rbac.PermSubjectRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	args := append([]any{}, store.ScopeArgs(rbac.ScopeOf(ActorFrom(r.Context())))...)
	rows, err := d.Store.Read().QueryContext(r.Context(), `
		SELECT id, name, note, disabled, frozen, expires_at, quota_bytes, quota_used_bytes, subscription_token, created_at, updated_at
		FROM subjects
		WHERE `+store.SubjectScopeSQL+`
		ORDER BY created_at DESC
	`, args...)
	if err != nil {
		http.Error(w, "failed to query subjects", http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="subjects.csv"`)

	csvWriter := csv.NewWriter(w)
	defer csvWriter.Flush()

	// Write header
	_ = csvWriter.Write([]string{
		"ID", "Name", "Note", "Disabled", "Frozen", "ExpiresAt", "QuotaBytes", "QuotaUsedBytes", "SubscriptionToken", "CreatedAt", "UpdatedAt",
	})

	for rows.Next() {
		var id int64
		var name string
		var note sql.NullString
		var disabled, frozen bool
		var expiresAt, quotaBytes, quotaUsedBytes sql.NullInt64
		var subscriptionToken string
		var createdAt, updatedAt int64

		err := rows.Scan(&id, &name, &note, &disabled, &frozen, &expiresAt, &quotaBytes, &quotaUsedBytes, &subscriptionToken, &createdAt, &updatedAt)
		if err != nil {
			continue // Skip bad rows
		}

		record := []string{
			strconv.FormatInt(id, 10),
			name,
			note.String,
			strconv.FormatBool(disabled),
			strconv.FormatBool(frozen),
			formatNullInt64(expiresAt),
			formatNullInt64(quotaBytes),
			formatNullInt64(quotaUsedBytes),
			subscriptionToken,
			time.Unix(createdAt, 0).Format(time.RFC3339),
			time.Unix(updatedAt, 0).Format(time.RFC3339),
		}

		_ = csvWriter.Write(record)
	}
	if err := rows.Err(); err != nil {
		// A mid-iteration failure would ship a truncated export that looks
		// complete, which is the worst outcome for a data dump: nobody checks
		// a CSV row count against a number they do not have.
		slog.ErrorContext(r.Context(), "subject export truncated", "error", err)
	}
}

func formatNullInt64(n sql.NullInt64) string {
	if n.Valid {
		return strconv.FormatInt(n.Int64, 10)
	}
	return ""
}
