package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/amyrm/antimage/internal/panel/deployment"
	"github.com/go-chi/chi/v5"
)

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

	validator := deployment.NewValidator(d.Store)
	result, err := validator.ValidateRevision(r.Context(), req.NodeID, req.Revision)
	if err != nil {
		slog.ErrorContext(r.Context(), "validate revision", "error", err, "admin_id", actor.AdminID)
		http.Error(w, "validation failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
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
	json.NewEncoder(w).Encode(map[string]interface{}{
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

	go func() {
		if err := orchestrator.ExecuteDeployment(r.Context(), deploymentID); err != nil {
			slog.ErrorContext(r.Context(), "execute deployment", "error", err, "deployment_id", deploymentID)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
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
	defer rows.Close()

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deployment":  dep,
		"node_status": nodeStatuses,
	})
}

func (d Deps) handleDeploymentList(w http.ResponseWriter, r *http.Request) {
	_, ok := requireActor(w, r)
	if !ok {
		return
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT id, revision_id, strategy, status, created_by, created_at, started_at, completed_at, error
		 FROM deployments ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		slog.ErrorContext(r.Context(), "list deployments", "error", err)
		http.Error(w, "failed to list deployments", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
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

	orchestrator := deployment.NewOrchestrator(d.Store)
	if err := orchestrator.RollbackDeployment(r.Context(), deploymentID); err != nil {
		slog.ErrorContext(r.Context(), "rollback deployment", "error", err, "admin_id", actor.AdminID, "deployment_id", deploymentID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deployment_id": deploymentID,
		"status":        "rolled_back",
	})
}
