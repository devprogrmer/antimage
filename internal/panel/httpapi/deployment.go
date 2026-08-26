package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/deployment"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/go-chi/chi/v5"
)

// nodeOfDeployment resolves the node a deployment changed, so the caller can
// be checked against it.
//
// A deployment with no node -- one recorded before 00032 added the column, on a
// panel with more than one node -- resolves to nothing and is refused. That is
// the fail-closed reading: "we cannot tell which node this touched" is not a
// reason to let anyone touch it.
func (d Deps) nodeOfDeployment(r *http.Request, deploymentID int64) (int64, bool) {
	var nodeID sql.NullInt64
	err := d.Store.Read().QueryRowContext(r.Context(),
		`SELECT node_id FROM deployments WHERE id = ?`, deploymentID).Scan(&nodeID)
	if err != nil || !nodeID.Valid {
		return 0, false
	}
	return nodeID.Int64, true
}

func (d Deps) handleDeploymentValidate(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	var req struct {
		NodeID   int64 `json:"node_id"`
		Revision int64 `json:"revision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Scoped to the node in the body. Validating a revision reads that node's
	// desired state, so it is a node read and is gated like one.
	if !d.requirePermission(w, r, rbac.PermNodeRead,
		rbac.Target{Kind: rbac.TargetNode, ID: req.NodeID}) {
		return
	}

	validator := deployment.NewValidator(d.Store)
	result, err := validator.ValidateRevision(r.Context(), req.NodeID, req.Revision)
	if err != nil {
		slog.ErrorContext(r.Context(), "validate revision", "error", err, "admin_id", actor.AdminID)
		http.Error(w, "validation failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (d Deps) handleDeploymentPreview(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	var req struct {
		NodeID   int64 `json:"node_id"`
		Revision int64 `json:"revision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Preview returns the node's revision numbers and document hashes, so it
	// is a node read and is scoped to that node.
	if !d.requirePermission(w, r, rbac.PermNodeRead,
		rbac.Target{Kind: rbac.TargetNode, ID: req.NodeID}) {
		return
	}

	var docSHA256 string
	err := d.Store.Read().QueryRowContext(r.Context(),
		`SELECT doc_sha256 FROM node_revisions WHERE node_id = ? AND revision = ?`,
		req.NodeID, req.Revision).Scan(&docSHA256)
	if err != nil {
		slog.ErrorContext(r.Context(), "get revision", "error", err, "admin_id", actor.AdminID)
		http.Error(w, "revision not found", http.StatusNotFound)
		return
	}

	var currentRevision int64
	var currentDocSHA256 string
	err = d.Store.Read().QueryRowContext(r.Context(),
		`SELECT revision, doc_sha256 FROM node_revisions
		 WHERE node_id = ? ORDER BY revision DESC LIMIT 1`,
		req.NodeID).Scan(&currentRevision, &currentDocSHA256)
	if err != nil {
		slog.ErrorContext(r.Context(), "get current revision", "error", err, "admin_id", actor.AdminID)
		http.Error(w, "failed to get current state", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"node_id":            req.NodeID,
		"current_revision":   currentRevision,
		"target_revision":    req.Revision,
		"current_doc_sha256": currentDocSHA256,
		"target_doc_sha256":  docSHA256,
	})
}

func (d Deps) handleDeploymentCreate(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	var req struct {
		NodeID   int64               `json:"node_id"`
		Strategy deployment.Strategy `json:"strategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Strategy == "" {
		req.Strategy = deployment.StrategyAllAtOnce
	}

	// Scoped to the node being deployed. TargetNone here was a permission gate
	// with no scope: an admin holding node:write for their own nodes could
	// deploy somebody else's.
	if !d.requirePermission(w, r, rbac.PermNodeWrite,
		rbac.Target{Kind: rbac.TargetNode, ID: req.NodeID}) {
		return
	}

	validStrategies := map[deployment.Strategy]bool{
		deployment.StrategyAllAtOnce: true,
		deployment.StrategyCanary:    true,
		deployment.StrategyStaged:    true,
		deployment.StrategyRolling:   true,
	}
	if !validStrategies[req.Strategy] {
		http.Error(w, "invalid strategy", http.StatusBadRequest)
		return
	}

	orchestrator := deployment.NewOrchestrator(d.Store)
	deploymentID, err := orchestrator.CreateDeployment(r.Context(), req.NodeID, req.Strategy, actor.AdminID)
	if err != nil {
		slog.ErrorContext(r.Context(), "create deployment", "error", err, "admin_id", actor.AdminID, "node_id", req.NodeID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// context.WithoutCancel, not r.Context(): the request context is cancelled
	// the moment this handler returns its 201, which would kill the deployment
	// it just started. The values (request id, actor) are kept for logging.
	bg := context.WithoutCancel(r.Context())
	go func() {
		if err := orchestrator.ExecuteDeployment(bg, deploymentID); err != nil {
			slog.ErrorContext(bg, "execute deployment", "error", err, "deployment_id", deploymentID)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"deployment_id": deploymentID,
		"status":        "pending",
	})
}

func (d Deps) handleDeploymentGet(w http.ResponseWriter, r *http.Request) {
	_, ok := requireActor(w, r)
	if !ok {
		return
	}

	deploymentIDStr := chi.URLParam(r, "id")
	if deploymentIDStr == "" {
		http.Error(w, "missing deployment id", http.StatusBadRequest)
		return
	}

	deploymentID, err := strconv.ParseInt(deploymentIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid deployment id", http.StatusBadRequest)
		return
	}

	// Two checks, in this order, and the order is the point.
	//
	// First: does this actor hold PermNodeRead AT ALL? An actor who does not is
	// refused before the deployment is looked up, so they cannot tell an id
	// that exists from one that does not by comparing 403 against 404.
	//
	// Then: the node the deployment changed, and whether it is in scope.
	if !d.requirePermission(w, r, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	nodeID, ok := d.nodeOfDeployment(r, deploymentID)
	if !ok {
		WriteError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if !d.requirePermission(w, r, rbac.PermNodeRead,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var dep struct {
		ID          int64  `json:"id"`
		RevisionID  int64  `json:"revision_id"`
		Strategy    string `json:"strategy"`
		Status      string `json:"status"`
		CreatedBy   int64  `json:"created_by"`
		CreatedAt   int64  `json:"created_at"`
		StartedAt   *int64 `json:"started_at"`
		CompletedAt *int64 `json:"completed_at"`
		Error       string `json:"error"`
	}

	err = d.Store.Read().QueryRowContext(r.Context(),
		`SELECT id, revision_id, strategy, status, created_by, created_at, started_at, completed_at, error
		 FROM deployments WHERE id = ?`,
		deploymentID,
	).Scan(&dep.ID, &dep.RevisionID, &dep.Strategy, &dep.Status, &dep.CreatedBy, &dep.CreatedAt, &dep.StartedAt, &dep.CompletedAt, &dep.Error)
	if err != nil {
		slog.ErrorContext(r.Context(), "get deployment", "error", err, "deployment_id", deploymentID)
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT node_id, status, started_at, completed_at, error
		 FROM deployment_node_status WHERE deployment_id = ?
		 ORDER BY node_id`,
		deploymentID)
	if err != nil {
		slog.ErrorContext(r.Context(), "get node status", "error", err, "deployment_id", deploymentID)
		http.Error(w, "failed to get node status", http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	type nodeStatus struct {
		NodeID      int64  `json:"node_id"`
		Status      string `json:"status"`
		StartedAt   *int64 `json:"started_at"`
		CompletedAt *int64 `json:"completed_at"`
		Error       string `json:"error"`
	}

	var nodeStatuses []nodeStatus
	for rows.Next() {
		var ns nodeStatus
		if err := rows.Scan(&ns.NodeID, &ns.Status, &ns.StartedAt, &ns.CompletedAt, &ns.Error); err != nil {
			slog.ErrorContext(r.Context(), "scan node status", "error", err)
			continue
		}
		nodeStatuses = append(nodeStatuses, ns)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(r.Context(), "rows error", "error", err)
		// A mid-iteration failure would return a truncated list as if complete.
		WriteError(w, http.StatusInternalServerError, "internal", "could not read rows")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"deployment":  dep,
		"node_status": nodeStatuses,
	})
}

func (d Deps) handleDeploymentList(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	// This route had no permission check and no filter: it returned every
	// deployment on the platform -- which node changed, when, by which admin,
	// and the text of any failure -- to anyone holding a session.
	if !d.requirePermission(w, r, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	// Joined to nodes so the shared scope predicate applies. A deployment whose
	// node is NULL -- recorded before 00032, on a panel with more than one node
	// -- is dropped by the join rather than shown to everyone, which is the
	// fail-closed direction for a row nobody can attribute.
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT d.id, d.revision_id, d.strategy, d.status, d.created_by,
		        d.created_at, d.started_at, d.completed_at, d.error
		   FROM deployments d
		   JOIN nodes ON nodes.id = d.node_id
		  WHERE `+store.NodeScopeSQL+`
		  ORDER BY d.created_at DESC LIMIT 100`,
		store.ScopeArgs(rbac.ScopeOf(actor))...)
	if err != nil {
		slog.ErrorContext(r.Context(), "list deployments", "error", err)
		http.Error(w, "failed to list deployments", http.StatusInternalServerError)
		return
	}
	defer func() { _ = rows.Close() }()

	type deploymentRow struct {
		ID          int64  `json:"id"`
		RevisionID  int64  `json:"revision_id"`
		Strategy    string `json:"strategy"`
		Status      string `json:"status"`
		CreatedBy   int64  `json:"created_by"`
		CreatedAt   int64  `json:"created_at"`
		StartedAt   *int64 `json:"started_at"`
		CompletedAt *int64 `json:"completed_at"`
		Error       string `json:"error"`
	}

	var deployments []deploymentRow
	for rows.Next() {
		var d deploymentRow
		if err := rows.Scan(&d.ID, &d.RevisionID, &d.Strategy, &d.Status, &d.CreatedBy, &d.CreatedAt, &d.StartedAt, &d.CompletedAt, &d.Error); err != nil {
			slog.ErrorContext(r.Context(), "scan deployment", "error", err)
			continue
		}
		deployments = append(deployments, d)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(r.Context(), "rows error", "error", err)
		// A mid-iteration failure would return a truncated list as if complete.
		WriteError(w, http.StatusInternalServerError, "internal", "could not read rows")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"deployments": deployments,
	})
}

func (d Deps) handleDeploymentRollback(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	deploymentIDStr := chi.URLParam(r, "id")
	if deploymentIDStr == "" {
		http.Error(w, "missing deployment id", http.StatusBadRequest)
		return
	}

	deploymentID, err := strconv.ParseInt(deploymentIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid deployment id", http.StatusBadRequest)
		return
	}

	// Two checks, in this order, and the order is the point.
	//
	// First: does this actor hold PermNodeWrite AT ALL? An actor who does not is
	// refused before the deployment is looked up, so they cannot tell an id
	// that exists from one that does not by comparing 403 against 404.
	//
	// Then: the node the deployment changed, and whether it is in scope.
	if !d.requirePermission(w, r, rbac.PermNodeWrite, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	nodeID, ok := d.nodeOfDeployment(r, deploymentID)
	if !ok {
		WriteError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	if !d.requirePermission(w, r, rbac.PermNodeWrite,
		rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	orchestrator := deployment.NewOrchestrator(d.Store)
	if err := orchestrator.RollbackDeployment(r.Context(), deploymentID); err != nil {
		slog.ErrorContext(r.Context(), "rollback deployment", "error", err, "admin_id", actor.AdminID, "deployment_id", deploymentID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"deployment_id": deploymentID,
		"status":        "rolled_back",
	})
}
