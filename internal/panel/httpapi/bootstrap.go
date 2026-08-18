package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

// maxAuditedOutput caps what a bootstrap run may write into the audit log. A
// failing install script can emit megabytes; the audit log is append-only and
// shared, so one bad run must not be able to bloat it.
const maxAuditedOutput = 4 << 10

type sshBootstrapRequest struct {
	Host          string `json:"host"`
	Port          int    `json:"port"`
	User          string `json:"user"`
	PrivateKeyPEM string `json:"private_key_pem"`
	Passphrase    string `json:"passphrase"`
	// HostKeyFingerprint is empty on the first call, which returns the
	// server's fingerprint for the admin to confirm; the second call supplies
	// the confirmed value.
	HostKeyFingerprint string `json:"host_key_fingerprint"`
}

// redactToken removes an enrollment token from text bound for the audit log.
//
// The bootstrap command carries `--token <token>`, and shell output echoes it
// readily enough — sudo logging, `set -x`, a curl error quoting its argv. The
// audit log is readable by every holder of audit:read, which is a wider
// audience than the one admin who supplied the SSH credentials and already
// knows the token. The HTTP response is not redacted: that is the operator's
// own stderr, and they are the party that just minted the token.
func redactToken(text, token string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "<redacted>")
}

func truncateForAudit(text string) string {
	if len(text) <= maxAuditedOutput {
		return text
	}
	return text[:maxAuditedOutput] + "\n[truncated]"
}

// handleSSHBootstrap runs the two-phase flow: read and confirm the host key,
// then execute. Credentials live only for this request and are wiped before
// the handler returns.
func (d Deps) handleSSHBootstrap(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	nodeID, err := pathInt64(r, "nodeID")
	if err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "invalid node id")
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeEnroll, rbac.Target{Kind: rbac.TargetNode, ID: nodeID}) {
		return
	}

	var req sshBootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	creds := nodes.SSHCredentials{
		Host: req.Host, Port: req.Port, User: req.User,
		PrivateKeyPEM: []byte(req.PrivateKeyPEM),
		Passphrase:    []byte(req.Passphrase),
	}
	// Wiped before this handler returns, on every path including panics.
	defer creds.Zero()

	ctx := r.Context()

	// Phase one: read the key and hand it back for a human to confirm.
	// Nothing is executed on a host whose fingerprint nobody has approved.
	if req.HostKeyFingerprint == "" {
		fingerprint, err := nodes.HostKeyPrompt(ctx, creds)
		if err != nil {
			WriteError(w, http.StatusBadGateway, "ssh_failed", "could not read the host key")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"host_key_fingerprint": fingerprint,
			"confirm_required":     true,
		})
		return
	}

	// Phase two needs the CA fingerprint to pin into the node's config. Deps.CA
	// is nil until the panel entrypoint builds one, and a nil dereference here
	// would panic inside a handler that is holding SSH private keys.
	if d.CA == nil {
		WriteError(w, http.StatusServiceUnavailable, "unavailable", "CA not initialised")
		return
	}

	token, err := nodes.IssueEnrollToken(ctx, d.Store, nodeID,
		d.actorAudit(actor, r), RequestID(ctx), d.now())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not issue token")
		return
	}

	command := "curl -fsSL https://" + r.Host + "/install.sh | sudo bash -s -- " +
		"--panel https://" + r.Host + " --token " + token +
		" --ca-fingerprint " + d.CA.FingerprintSHA256()

	output, runErr := nodes.BootstrapOverSSH(ctx, creds, req.HostKeyFingerprint, command)

	// Audited either way. Installing an agent on a new host over SSH is a
	// privileged act; recording only the failures would leave the successful
	// ones — the ones that actually changed a machine — invisible.
	result := "ok"
	if runErr != nil {
		result = "failed"
	}
	audit.BestEffort(ctx, d.Store, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
		Action:     "node.bootstrap",
		TargetType: "node",
		TargetID:   sql.NullInt64{Int64: nodeID, Valid: true},
		Result:     result,
		After: map[string]any{
			"host":   req.Host,
			"user":   req.User,
			"output": truncateForAudit(redactToken(output, token)),
		},
	})

	if runErr != nil {
		// The real stderr is what an operator needs to fix it, and it goes
		// only to the admin who supplied the credentials.
		WriteJSON(w, http.StatusBadGateway, map[string]any{
			"error":  map[string]string{"code": "bootstrap_failed", "message": runErr.Error()},
			"output": output,
		})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"output": output})
}
