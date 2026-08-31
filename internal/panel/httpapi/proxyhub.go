package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// Proxy Hub: third-party proxy/VPN provider accounts (Cloudflare WARP,
// NordVPN today) an operator registers once, then uses to provision real
// WireGuard outbounds on any node. Gated on outbound:* -- provisioning one
// of these ultimately creates a normal outbound row, the same "where does
// traffic go" trust boundary egress and balancers already use.
//
// Registering an account is deliberately NOT node-scoped: the credential
// belongs to the operator, not to any one node, and the same WARP device or
// NordVPN token can back outbounds on several nodes without re-registering.
// Provisioning FROM an account (see proxyhub_warp.go / proxyhub_nordvpn.go)
// is the node-scoped step.

// proxyProviderAccountDTO is deliberately credential-free: Credentials never
// appears here, on any code path, for any caller. A private key or access
// token that reaches the browser is a secret the panel can no longer claim
// to protect, regardless of how carefully every OTHER field is redacted.
type proxyProviderAccountDTO struct {
	ID        int64          `json:"id"`
	Provider  string         `json:"provider"`
	Label     string         `json:"label"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt int64          `json:"created_at"`
}

func (d Deps) handleListProxyProviderAccounts(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundRead, rbac.Target{}) {
		return
	}

	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT id, provider, label, metadata, created_at
		   FROM proxy_provider_accounts ORDER BY id`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list proxy provider accounts")
		return
	}
	defer func() { _ = rows.Close() }()

	out := []proxyProviderAccountDTO{}
	for rows.Next() {
		var dto proxyProviderAccountDTO
		var metadata string
		if err := rows.Scan(&dto.ID, &dto.Provider, &dto.Label, &metadata, &dto.CreatedAt); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read proxy provider account")
			return
		}
		if err := json.Unmarshal([]byte(metadata), &dto.Metadata); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not decode proxy provider metadata")
			return
		}
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list proxy provider accounts")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func (d Deps) handleDeleteProxyProviderAccount(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermOutboundWrite, rbac.Target{}) {
		return
	}
	accountID, err := pathInt64(r, "accountID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid account id")
		return
	}

	// Deleting the account does not touch outbounds already provisioned from
	// it -- those are independent rows on independent nodes by this point,
	// exactly as a WireGuard outbound entered by hand keeps working after
	// whatever notes an operator kept about it are gone. It only prevents
	// provisioning further outbounds from this credential.
	ctx := r.Context()
	var found bool
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx,
			`DELETE FROM proxy_provider_accounts WHERE id = ?`, accountID)
		if execErr != nil {
			return execErr
		}
		n, execErr := res.RowsAffected()
		if execErr != nil {
			return execErr
		}
		found = n > 0
		return nil
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not delete proxy provider account")
		return
	}
	if !found {
		WriteError(w, http.StatusNotFound, "not_found", "proxy provider account not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// storeProxyProviderAccount seals credentials and inserts a new account row,
// shared by every provider's registration handler.
func (d Deps) storeProxyProviderAccount(
	r *http.Request, provider, label string, credentials, metadata any,
) (proxyProviderAccountDTO, error) {
	credJSON, err := json.Marshal(credentials)
	if err != nil {
		return proxyProviderAccountDTO{}, err
	}
	sealed, err := nodes.SealOutboundParams(d.Box, credJSON)
	if err != nil {
		return proxyProviderAccountDTO{}, err
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return proxyProviderAccountDTO{}, err
	}

	ctx := r.Context()
	var id int64
	now := d.now().Unix()
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		res, execErr := tx.ExecContext(ctx,
			`INSERT INTO proxy_provider_accounts (provider, label, credentials, metadata, created_at, updated_at)
			 VALUES (?,?,?,?,?,?)`,
			provider, label, sealed, string(metaJSON), now, now)
		if execErr != nil {
			return execErr
		}
		id, execErr = res.LastInsertId()
		return execErr
	})
	if err != nil {
		return proxyProviderAccountDTO{}, err
	}

	var meta map[string]any
	_ = json.Unmarshal(metaJSON, &meta)
	return proxyProviderAccountDTO{
		ID: id, Provider: provider, Label: label, Metadata: meta, CreatedAt: now,
	}, nil
}

// loadProxyProviderAccount reads and unseals one account's credentials,
// refusing a provider mismatch so a NordVPN account id can never be handed
// to WARP's provisioning path (or vice versa) by a caller passing the wrong
// account for the URL they hit.
func (d Deps) loadProxyProviderAccount(ctx context.Context, accountID int64, wantProvider string, into any) (label string, err error) {
	var provider, sealed string
	err = d.Store.Read().QueryRowContext(ctx,
		`SELECT provider, label, credentials FROM proxy_provider_accounts WHERE id = ?`, accountID).
		Scan(&provider, &label, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errProxyProviderAccountNotFound
	}
	if err != nil {
		return "", err
	}
	if provider != wantProvider {
		return "", errProxyProviderAccountWrongKind
	}
	raw, err := nodes.OpenOutboundParams(d.Box, sealed)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return "", err
	}
	return label, nil
}

var errProxyProviderAccountNotFound = errors.New("proxy provider account not found")
var errProxyProviderAccountWrongKind = errors.New("proxy provider account is a different provider")
