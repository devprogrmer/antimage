package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type hostDTO struct {
	ID            int64  `json:"id"`
	ServiceID     int64  `json:"service_id"`
	NodeID        int64  `json:"node_id"`
	NodeName      string `json:"node_name"`
	Remark        string `json:"remark"`
	Address       string `json:"address"`
	Port          *int64 `json:"port"`
	SNI           string `json:"sni"`
	Host          string `json:"host"`
	Path          string `json:"path"`
	Security      string `json:"security"`
	Fingerprint   string `json:"fingerprint"`
	ALPN          string `json:"alpn"`
	AllowInsecure bool   `json:"allow_insecure"`
	PublicKey     string `json:"public_key"`
	ShortID       string `json:"short_id"`
	SpiderX       string `json:"spider_x"`
	Flow          string `json:"flow"`
	Enabled       bool   `json:"enabled"`
	Priority      int    `json:"priority"`
	CreatedAt     int64  `json:"created_at"`
}

type hostWriteRequest struct {
	ServiceID     int64  `json:"service_id"`
	Remark        string `json:"remark"`
	Address       string `json:"address"`
	Port          *int64 `json:"port"`
	SNI           string `json:"sni"`
	Host          string `json:"host"`
	Path          string `json:"path"`
	Security      string `json:"security"`
	Fingerprint   string `json:"fingerprint"`
	ALPN          string `json:"alpn"`
	AllowInsecure bool   `json:"allow_insecure"`
	PublicKey     string `json:"public_key"`
	ShortID       string `json:"short_id"`
	SpiderX       string `json:"spider_x"`
	Flow          string `json:"flow"`
	Enabled       *bool  `json:"enabled"`
	Priority      int    `json:"priority"`
}

func (d Deps) handleListHosts(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermServiceRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	ctx := r.Context()
	args := store.ScopeArgs(rbac.ScopeOf(actor))
	q := `SELECT h.id, h.service_id, s.node_id, nodes.name, h.remark, h.address, h.port,
	             h.sni, h.host, h.path, h.security, h.fingerprint, h.alpn, h.allow_insecure,
	             h.public_key, h.short_id, h.spider_x, h.flow, h.enabled, h.priority, h.created_at
	        FROM subscription_hosts h
	        JOIN services s ON s.id = h.service_id
	        JOIN nodes ON nodes.id = s.node_id
	       WHERE ` + store.NodeScopeSQL + `
	       ORDER BY nodes.name, h.priority, h.id`
	rows, err := d.Store.Read().QueryContext(ctx, q, args...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list hosts")
		return
	}
	defer func() { _ = rows.Close() }()
	out := make([]hostDTO, 0)
	for rows.Next() {
		dto, err := scanHost(rows)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read hosts")
			return
		}
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read hosts")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

func scanHost(row interface{ Scan(dest ...any) error }) (hostDTO, error) {
	var (
		dto               hostDTO
		port              sql.NullInt64
		insecure, enabled int
	)
	err := row.Scan(&dto.ID, &dto.ServiceID, &dto.NodeID, &dto.NodeName, &dto.Remark, &dto.Address, &port,
		&dto.SNI, &dto.Host, &dto.Path, &dto.Security, &dto.Fingerprint, &dto.ALPN, &insecure,
		&dto.PublicKey, &dto.ShortID, &dto.SpiderX, &dto.Flow, &enabled, &dto.Priority, &dto.CreatedAt)
	if err != nil {
		return hostDTO{}, err
	}
	if port.Valid {
		dto.Port = &port.Int64
	}
	dto.AllowInsecure = insecure == 1
	dto.Enabled = enabled == 1
	return dto, nil
}

func (d Deps) lookupServiceNode(r *http.Request, serviceID int64) (int64, bool) {
	var nodeID int64
	err := d.Store.Read().QueryRowContext(r.Context(),
		`SELECT node_id FROM services WHERE id = ?`, serviceID).Scan(&nodeID)
	if err != nil {
		return 0, false
	}
	return nodeID, true
}

func (d Deps) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	var req hostWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if req.ServiceID == 0 {
		WriteError(w, http.StatusUnprocessableEntity, "validation", "service_id is required")
		return
	}
	if msg := validateHostSecurity(req.Security); msg != "" {
		WriteError(w, http.StatusUnprocessableEntity, "validation", msg)
		return
	}
	nodeID, found := d.lookupServiceNode(r, req.ServiceID)
	if !found {
		WriteError(w, http.StatusNotFound, "not_found", "service not found")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}
	enabled := 1
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	}
	insecure := 0
	if req.AllowInsecure {
		insecure = 1
	}
	now := d.now().Unix()
	ctx := r.Context()
	var id int64
	err := d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO subscription_hosts (
				service_id, remark, address, port, sni, host, path, security,
				fingerprint, alpn, allow_insecure, public_key, short_id, spider_x, flow,
				enabled, priority, created_at
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			req.ServiceID, strings.TrimSpace(req.Remark), strings.TrimSpace(req.Address), req.Port,
			strings.TrimSpace(req.SNI), strings.TrimSpace(req.Host), strings.TrimSpace(req.Path),
			strings.TrimSpace(req.Security), strings.TrimSpace(req.Fingerprint), strings.TrimSpace(req.ALPN),
			insecure, strings.TrimSpace(req.PublicKey), strings.TrimSpace(req.ShortID),
			strings.TrimSpace(req.SpiderX), strings.TrimSpace(req.Flow), enabled, req.Priority, now)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "host.create", TargetType: "host",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
			After: map[string]any{"service_id": req.ServiceID, "address": req.Address},
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not create host")
		return
	}
	WriteJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (d Deps) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "hostID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid host id")
		return
	}
	var req hostWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if errMsg := validateHostSecurity(req.Security); errMsg != "" {
		WriteError(w, http.StatusUnprocessableEntity, "validation", errMsg)
		return
	}
	var nodeID int64
	scanErr := d.Store.Read().QueryRowContext(r.Context(),
		`SELECT s.node_id FROM subscription_hosts h
		   JOIN services s ON s.id = h.service_id WHERE h.id = ?`, id).Scan(&nodeID)
	if scanErr != nil {
		WriteError(w, http.StatusNotFound, "not_found", "host not found")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}
	enabled := 1
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	}
	insecure := 0
	if req.AllowInsecure {
		insecure = 1
	}
	ctx := r.Context()
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE subscription_hosts SET
				remark=?, address=?, port=?, sni=?, host=?, path=?, security=?,
				fingerprint=?, alpn=?, allow_insecure=?, public_key=?, short_id=?, spider_x=?, flow=?,
				enabled=?, priority=?
			 WHERE id = ?`,
			strings.TrimSpace(req.Remark), strings.TrimSpace(req.Address), req.Port,
			strings.TrimSpace(req.SNI), strings.TrimSpace(req.Host), strings.TrimSpace(req.Path),
			strings.TrimSpace(req.Security), strings.TrimSpace(req.Fingerprint), strings.TrimSpace(req.ALPN),
			insecure, strings.TrimSpace(req.PublicKey), strings.TrimSpace(req.ShortID),
			strings.TrimSpace(req.SpiderX), strings.TrimSpace(req.Flow), enabled, req.Priority, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "host.update", TargetType: "host",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not update host")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "hostID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid host id")
		return
	}
	var nodeID int64
	scanErr := d.Store.Read().QueryRowContext(r.Context(),
		`SELECT s.node_id FROM subscription_hosts h
		   JOIN services s ON s.id = h.service_id WHERE h.id = ?`, id).Scan(&nodeID)
	if scanErr != nil {
		WriteError(w, http.StatusNotFound, "not_found", "host not found")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermServiceWrite, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}
	ctx := r.Context()
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM subscription_hosts WHERE id = ?`, id); err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action: "host.delete", TargetType: "host",
			TargetID: sql.NullInt64{Int64: id, Valid: true}, Result: "ok",
		})
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete host")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateHostSecurity(sec string) string {
	switch strings.TrimSpace(sec) {
	case "", "none", "tls", "reality":
		return ""
	default:
		return "security must be empty, none, tls, or reality"
	}
}
