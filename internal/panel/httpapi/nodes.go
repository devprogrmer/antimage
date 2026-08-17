package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// actorAudit projects the already-resolved actor onto an audit actor.
//
// It takes the actor rather than digging it back out of the context because
// the context lookup can return nil, and dereferencing that would turn a
// routing mistake into a panic inside a write transaction. Handlers resolve
// the actor once, up front, with requireActor.
func (d Deps) actorAudit(a *rbac.Actor, r *http.Request) audit.Actor {
	return audit.AdminActor(a.AdminID, clientIP(r))
}

func pathInt64(r *http.Request, key string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, key), 10, 64)
}

// targetTypeName maps an authorization target onto the audit log's
// target_type vocabulary. TargetNone has no subject, so it stays empty.
func targetTypeName(k rbac.TargetKind) string {
	switch k {
	case rbac.TargetNode:
		return "node"
	case rbac.TargetService:
		return "service"
	default:
		return ""
	}
}

// authorize is the single chokepoint every handler calls. The denial message
// is identical for "no permission" and "out of scope": distinguishing them
// would let a reseller probe for the existence of another's node.
//
// Every denial is recorded with audit.BestEffort per spec invariant 9: a
// rejected attempt never commits, so it has no transaction to ride along in,
// and it is precisely the event that must not be lost. Reads are otherwise
// unaudited because successful reads are noise; a denied read is not a read,
// it is a probe, so the permission involved does not change the decision to
// record it.
//
// BestEffort takes the store's single write connection. Calling it from
// inside a store.Write callback on the same store would deadlock until its
// own timeout, so this must stay on the pre-write path — which it is: every
// caller authorizes before it opens a transaction.
func (d Deps) authorize(w http.ResponseWriter, r *http.Request,
	a *rbac.Actor, p rbac.Permission, t rbac.Target) bool {
	if err := rbac.Check(a, p, t); err == nil {
		return true
	}
	ctx := r.Context()
	rec := audit.Record{
		Action:     "authz.deny",
		TargetType: targetTypeName(t.Kind),
		Result:     "denied",
		After: map[string]any{
			"permission": string(p),
			"method":     r.Method,
			"path":       r.URL.Path,
		},
	}
	if t.Kind != rbac.TargetNone {
		rec.TargetID = sql.NullInt64{Int64: t.ID, Valid: true}
	}
	// rbac.Check also rejects a nil actor. Handlers use requireActor so that
	// should be unreachable, but the audit row must not be the thing that
	// panics if it ever is: actor_type 'admin' requires an admin id.
	who := audit.Actor{Type: audit.ActorSystem, Label: "authz", IP: clientIP(r)}
	if a != nil {
		who = d.actorAudit(a, r)
	}
	audit.BestEffort(ctx, d.Store, RequestID(ctx), who, rec)

	WriteError(w, http.StatusForbidden, "forbidden", "permission denied")
	return false
}

// nodeSummary is the wire shape of a node row.
//
// It exists so the store's row struct never reaches the client verbatim:
// store.NodeRow carries sql.NullInt64, whose JSON encoding leaks the driver's
// representation, and a DTO keeps a future column from becoming an accidental
// part of the API.
type nodeSummary struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Address         string `json:"address"`
	Status          string `json:"status"`
	DesiredRevision int64  `json:"desired_revision"`
	AppliedRevision int64  `json:"applied_revision"`
	LastSeenAt      *int64 `json:"last_seen_at"`
	// Online is live stream membership, not a stored column: status says what
	// the last convergence looked like, online says whether the agent is
	// holding a control stream right now.
	Online bool `json:"online"`
}

func (d Deps) summarize(n store.NodeRow) nodeSummary {
	summary := nodeSummary{
		ID:              n.ID,
		Name:            n.Name,
		Address:         n.Address,
		Status:          n.Status,
		DesiredRevision: n.DesiredRevision,
		AppliedRevision: n.AppliedRevision,
		Online:          d.Hub.Online(n.ID),
	}
	if n.LastSeenAt.Valid {
		seen := n.LastSeenAt.Int64
		summary.LastSeenAt = &seen
	}
	return summary
}

// handleListNodes returns the nodes visible to the caller.
//
// Both enforcement layers from spec section 6.3 apply: rbac.Check gates the
// permission here, and the store filters rows by scope, so a caller with
// node:read still sees only their own nodes.
func (d Deps) handleListNodes(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	rows, err := d.Store.ListNodes(r.Context(), rbac.ScopeOf(actor))
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list nodes")
		return
	}

	out := make([]nodeSummary, 0, len(rows))
	for _, n := range rows {
		out = append(out, d.summarize(n))
	}
	WriteJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

func (d Deps) handleGetNode(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	node, err := d.Store.GetNode(r.Context(), rbac.ScopeOf(actor), id)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not load node")
		return
	}
	WriteJSON(w, http.StatusOK, d.summarize(*node))
}

func (d Deps) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	var req struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Address) == "" {
		WriteError(w, http.StatusUnprocessableEntity, "validation", "name and address are required")
		return
	}

	ctx := r.Context()
	var nodeID int64
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (name, address, created_at) VALUES (?,?,?)`,
			req.Name, req.Address, d.now().Unix())
		if err != nil {
			return err
		}
		nodeID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "node.create", TargetType: "node",
			TargetID: sql.NullInt64{Int64: nodeID, Valid: true},
			After:    map[string]any{"name": req.Name, "address": req.Address},
			Result:   "ok",
		})
	})
	if err != nil {
		if isUniqueViolation(err) {
			WriteError(w, http.StatusConflict, "conflict", "a node with that name already exists")
			return
		}
		WriteError(w, http.StatusInternalServerError, "internal", "could not create node")
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"id": nodeID})
}

// isUniqueViolation recognises SQLite's uniqueness failure.
//
// The driver does not export a sentinel for it, so the message is the only
// signal available. The match is case-insensitive and deliberately narrow:
// anything else stays a 500, because reporting an unrelated storage failure
// as a name collision would send the operator hunting for a duplicate that
// does not exist.
func isUniqueViolation(err error) bool {
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}

func (d Deps) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	ctx := r.Context()
	// Deleting the row removes cert_fingerprint, which is the allow-list, so
	// the node is locked out on its next connection attempt. The audit row
	// outlives it: audit_log.target_id carries no foreign key, by design.
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "node.delete", TargetType: "node",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete node")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleIssueEnrollToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeEnroll, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	now := d.now()
	token, err := nodes.IssueEnrollToken(r.Context(), d.Store, id,
		d.actorAudit(actor, r), RequestID(r.Context()), now)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not issue token")
		return
	}
	scheme := "https"
	command := fmt.Sprintf(
		"curl -fsSL %s://%s/install.sh | sudo bash -s -- --panel %s://%s --token %s",
		scheme, r.Host, scheme, r.Host, token)

	// Returned once and never retrievable again: only the hash is stored.
	WriteJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"command":    command,
		"expires_at": now.Add(nodes.EnrollTokenTTL).Unix(),
	})
}

func (d Deps) handleListRevisions(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT revision, created_at, actor_type, actor_label, reason, doc_sha256
		   FROM node_revisions WHERE node_id = ? ORDER BY revision DESC LIMIT 100`, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list revisions")
		return
	}
	defer func() { _ = rows.Close() }()

	type revisionDTO struct {
		Revision   int64  `json:"revision"`
		CreatedAt  int64  `json:"created_at"`
		ActorType  string `json:"actor_type"`
		ActorLabel string `json:"actor_label"`
		Reason     string `json:"reason"`
		SHA256     string `json:"sha256"`
	}
	out := []revisionDTO{}
	for rows.Next() {
		var rev revisionDTO
		if err := rows.Scan(&rev.Revision, &rev.CreatedAt, &rev.ActorType,
			&rev.ActorLabel, &rev.Reason, &rev.SHA256); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read revisions")
			return
		}
		out = append(out, rev)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read revisions")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

func (d Deps) handleListApplyRuns(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNode, ID: id}) {
		return
	}
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT r.id, r.target_revision, r.started_at, r.outcome,
		        COALESCE(s.seq,0), COALESCE(s.step_kind,''), COALESCE(s.disruption,''),
		        COALESCE(s.outcome,''), COALESCE(s.error,''), COALESCE(s.duration_ms,0)
		   FROM node_apply_runs r
		   LEFT JOIN node_apply_steps s ON s.run_id = r.id
		  WHERE r.node_id = ?
		  ORDER BY r.id DESC, s.seq ASC
		  LIMIT 500`, id)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list apply runs")
		return
	}
	defer func() { _ = rows.Close() }()

	type stepDTO struct {
		Seq        int    `json:"seq"`
		Kind       string `json:"kind"`
		Disruption string `json:"disruption"`
		Outcome    string `json:"outcome"`
		Error      string `json:"error"`
		DurationMS int64  `json:"duration_ms"`
	}
	type runDTO struct {
		ID             int64     `json:"id"`
		TargetRevision int64     `json:"target_revision"`
		StartedAt      int64     `json:"started_at"`
		Outcome        string    `json:"outcome"`
		Steps          []stepDTO `json:"steps"`
	}
	out := []runDTO{}
	byID := map[int64]int{}
	for rows.Next() {
		var (
			run  runDTO
			step stepDTO
		)
		if err := rows.Scan(&run.ID, &run.TargetRevision, &run.StartedAt, &run.Outcome,
			&step.Seq, &step.Kind, &step.Disruption, &step.Outcome,
			&step.Error, &step.DurationMS); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read apply runs")
			return
		}
		idx, seen := byID[run.ID]
		if !seen {
			run.Steps = []stepDTO{}
			out = append(out, run)
			idx = len(out) - 1
			byID[run.ID] = idx
		}
		// The LEFT JOIN yields one all-zero step row for a run with no steps
		// recorded yet; seq is 1-based, so zero means "no step here".
		if step.Seq != 0 {
			out[idx].Steps = append(out[idx].Steps, step)
		}
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read apply runs")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"runs": out})
}
