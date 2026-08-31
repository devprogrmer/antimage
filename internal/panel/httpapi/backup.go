package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// handleDownloadBackup writes a consistent SQLite copy. VACUUM INTO must not
// run inside store.Write (it rejects being inside a transaction).
func (d Deps) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermSettingsWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	tmp, err := os.CreateTemp("", "antimage-backup-*.db")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not create backup file")
		return
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(path) }()

	if _, err := d.Store.Read().ExecContext(r.Context(), `VACUUM INTO ?`, path); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not snapshot database")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read backup")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read backup")
		return
	}

	audit.BestEffort(r.Context(), d.Store, RequestID(r.Context()), d.actorAudit(actor, r), audit.Record{
		Action: "backup.download", TargetType: "settings", Result: "ok",
		After: map[string]any{"bytes": info.Size()},
	})

	name := fmt.Sprintf("antimage-%s.db", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(name)+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}
