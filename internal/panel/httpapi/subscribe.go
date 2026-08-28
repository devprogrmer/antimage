package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/subjects"
	"github.com/amyrm/antimage/internal/panel/subscriptions"
)

// handleSubscribe implements GET /api/v1/subscribe/{token}.
// This is a PUBLIC, UNAUTHENTICATED endpoint - the token IS the authentication.
func (d Deps) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	token := chi.URLParam(r, "token")

	if token == "" {
		http.Error(w, "missing token", http.StatusNotFound)
		return
	}

	if !subscriptionRateLimiter.Allow(token) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	subjectID, err := subjects.LookupByToken(ctx, d.Store, token)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	var (
		expiresAt sql.NullInt64
		frozenAt  sql.NullInt64
		name      string
	)
	row := d.Store.Read().QueryRowContext(ctx,
		`SELECT name, expires_at, frozen_at FROM subjects WHERE id = ?`, subjectID)
	if err := row.Scan(&name, &expiresAt, &frozenAt); err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	now := d.now().Unix()
	if expiresAt.Valid && expiresAt.Int64 <= now {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if frozenAt.Valid {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	servers, err := d.gatherServers(ctx, subjectID, name)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if len(servers) == 0 {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	format := subscriptions.DetectFormat(r.Header.Get("User-Agent"))
	if q := strings.TrimSpace(r.URL.Query().Get("format")); q != "" {
		switch strings.ToLower(q) {
		case "v2ray", "v2rayn", "links":
			format = subscriptions.FormatV2Ray
		case "clash", "meta", "clashmeta":
			format = subscriptions.FormatClash
		case "singbox", "sing-box", "sb":
			format = subscriptions.FormatSingBox
		}
	}

	var content []byte
	var contentType string
	switch format {
	case subscriptions.FormatV2Ray:
		content, contentType, err = (&subscriptions.V2RayRenderer{}).Render(ctx, servers)
	case subscriptions.FormatClash:
		content, contentType, err = (&subscriptions.ClashRenderer{}).Render(ctx, servers)
	case subscriptions.FormatSingBox:
		content, contentType, err = (&subscriptions.SingBoxRenderer{}).Render(ctx, servers)
	default:
		content, contentType, err = (&subscriptions.V2RayRenderer{}).Render(ctx, servers)
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Profile-Update-Interval", "6")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

type hostRow struct {
	ServiceID     int64
	Remark        string
	Address       string
	Port          sql.NullInt64
	SNI           string
	Host          string
	Path          string
	Security      string
	Fingerprint   string
	ALPN          string
	AllowInsecure int
	PublicKey     string
	ShortID       string
	SpiderX       string
	Flow          string
	Priority      int
}

func (d Deps) gatherServers(ctx context.Context, subjectID int64, subjectName string) ([]subscriptions.Server, error) {
	query := `
		SELECT
			n.id, n.name, n.address,
			s.id, s.adapter_kind, s.params
		FROM subject_services ss
		JOIN services s ON s.id = ss.service_id
		JOIN nodes n ON n.id = s.node_id
		WHERE ss.subject_id = ?
		  AND s.enabled = 1
		  AND n.status != 'disabled'
		ORDER BY n.name, s.id
	`

	rows, err := d.Store.Read().QueryContext(ctx, query, subjectID)
	if err != nil {
		return nil, fmt.Errorf("query servers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type serviceRow struct {
		nodeID      int64
		nodeName    string
		nodeAddress string
		serviceID   int64
		adapterKind string
		paramsJSON  string
	}
	var services []serviceRow
	serviceIDs := make([]int64, 0)
	for rows.Next() {
		var row serviceRow
		if err := rows.Scan(&row.nodeID, &row.nodeName, &row.nodeAddress,
			&row.serviceID, &row.adapterKind, &row.paramsJSON); err != nil {
			return nil, fmt.Errorf("scan server: %w", err)
		}
		services = append(services, row)
		serviceIDs = append(serviceIDs, row.serviceID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return nil, nil
	}

	uuidVal, passVal := d.unsealSubjectCreds(ctx, subjectID)

	hostsByService, err := d.hostsForServices(ctx, serviceIDs)
	if err != nil {
		return nil, err
	}

	settings, _ := d.loadPanelSettings(ctx)
	template := settings["remark_template"]

	var servers []subscriptions.Server
	for _, svc := range services {
		in := subscriptions.ParseInbound([]byte(svc.paramsJSON))
		base := subscriptions.Server{
			NodeID:      svc.nodeID,
			NodeName:    svc.nodeName,
			NodeAddress: svc.nodeAddress,
			ServiceID:   svc.serviceID,
			Protocol:    in.Protocol,
			Port:        in.Port,
			UUID:        uuidVal,
			Password:    passVal,
			TLS:         in.Security == "tls" || in.Security == "reality",
			Security:    in.Security,
			SNI:         in.SNI,
			Network:     in.Network,
			Path:        in.Path,
			Host:        in.Host,
			PublicKey:   in.PublicKey,
			Flow:        in.Flow,
		}
		if len(in.ShortIDs) > 0 {
			base.ShortID = in.ShortIDs[0]
		}

		hosts := hostsByService[svc.serviceID]
		if len(hosts) == 0 {
			base.Remark = applyRemarkTemplate(template, subjectName, base)
			servers = append(servers, base)
			continue
		}
		for _, h := range hosts {
			srv := overlayHost(base, h)
			if srv.Remark == "" {
				srv.Remark = applyRemarkTemplate(template, subjectName, srv)
			}
			servers = append(servers, srv)
		}
	}
	return servers, nil
}

func (d Deps) unsealSubjectCreds(ctx context.Context, subjectID int64) (uuid, password string) {
	if d.Box == nil {
		return "", ""
	}
	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT kind, value_enc FROM subject_credentials WHERE subject_id = ?`, subjectID)
	if err != nil {
		return "", ""
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var enc []byte
		if err := rows.Scan(&kind, &enc); err != nil {
			continue
		}
		plain, err := d.Box.Open(enc)
		if err != nil {
			continue
		}
		switch kind {
		case "uuid":
			uuid = string(plain)
		case "password":
			password = string(plain)
		}
	}
	return uuid, password
}

func (d Deps) hostsForServices(ctx context.Context, serviceIDs []int64) (map[int64][]hostRow, error) {
	out := make(map[int64][]hostRow)
	if len(serviceIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(serviceIDs))
	args := make([]any, len(serviceIDs))
	for i, id := range serviceIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `SELECT service_id, remark, address, port, sni, host, path, security,
	             fingerprint, alpn, allow_insecure, public_key, short_id, spider_x, flow, priority
	        FROM subscription_hosts
	       WHERE enabled = 1 AND service_id IN (` + strings.Join(placeholders, ",") + `)
	       ORDER BY priority, id`
	rows, err := d.Store.Read().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query hosts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h hostRow
		if err := rows.Scan(&h.ServiceID, &h.Remark, &h.Address, &h.Port, &h.SNI, &h.Host, &h.Path,
			&h.Security, &h.Fingerprint, &h.ALPN, &h.AllowInsecure, &h.PublicKey, &h.ShortID,
			&h.SpiderX, &h.Flow, &h.Priority); err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}
		out[h.ServiceID] = append(out[h.ServiceID], h)
	}
	return out, rows.Err()
}

func overlayHost(base subscriptions.Server, h hostRow) subscriptions.Server {
	srv := base
	if h.Remark != "" {
		srv.Remark = h.Remark
	}
	if h.Address != "" {
		srv.NodeAddress = h.Address
	}
	if h.Port.Valid && h.Port.Int64 > 0 {
		srv.Port = int(h.Port.Int64)
	}
	if h.SNI != "" {
		srv.SNI = h.SNI
	}
	if h.Host != "" {
		srv.Host = h.Host
	}
	if h.Path != "" {
		srv.Path = h.Path
	}
	if h.Security != "" {
		srv.Security = h.Security
		srv.TLS = h.Security == "tls" || h.Security == "reality"
	}
	if h.Fingerprint != "" {
		srv.Fingerprint = h.Fingerprint
	}
	if h.ALPN != "" {
		srv.ALPN = splitCSV(h.ALPN)
	}
	srv.AllowInsecure = h.AllowInsecure == 1
	if h.PublicKey != "" {
		srv.PublicKey = h.PublicKey
	}
	if h.ShortID != "" {
		srv.ShortID = h.ShortID
	}
	if h.SpiderX != "" {
		srv.SpiderX = h.SpiderX
	}
	if h.Flow != "" {
		srv.Flow = h.Flow
	}
	return srv
}

func applyRemarkTemplate(template, subjectName string, srv subscriptions.Server) string {
	if strings.TrimSpace(template) == "" {
		if srv.Remark != "" {
			return srv.Remark
		}
		return srv.NodeName
	}
	repl := strings.NewReplacer(
		"{name}", subjectName,
		"{node}", srv.NodeName,
		"{protocol}", srv.Protocol,
		"{port}", strconv.Itoa(srv.Port),
		"{address}", srv.NodeAddress,
	)
	return repl.Replace(template)
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// extractInboundConfig is kept for older tests that still call it.
func (d Deps) extractInboundConfig(params map[string]interface{}) (protocol string, port int) {
	raw, _ := json.Marshal(params)
	in := subscriptions.ParseInbound(raw)
	return in.Protocol, in.Port
}

var subscriptionRateLimiter = subscriptions.NewSlidingWindowLimiter(10, time.Minute)
