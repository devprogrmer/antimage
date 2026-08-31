package httpapi

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// PKI oversight: the CA, the certificates it has issued, and revocation.
//
// The panel is the fleet's certificate authority and the only verifier of node
// certificates, which makes this the one place an operator can answer "is any
// node about to fall off the control plane?". Certificates last a year
// (nodes.NodeCertLifetime) and agents renew at the halfway mark, so a
// certificate inside the warning window is not a routine countdown -- it means
// renewal has been failing for months.

// certExpiryWarning is how far ahead a certificate is called expiring.
//
// Thirty days rather than something longer precisely BECAUSE agents renew at
// six months. A warning that fires while auto-renewal is still expected to
// handle it is a warning operators learn to ignore; by thirty days out,
// renewal has demonstrably not happened and a human has to act.
const certExpiryWarning = 30 * 24 * time.Hour

type caCertDTO struct {
	Subject      string `json:"subject"`
	Issuer       string `json:"issuer"`
	NotBefore    int64  `json:"not_before"`
	NotAfter     int64  `json:"not_after"`
	SerialNumber string `json:"serial_number"`
	Fingerprint  string `json:"fingerprint"`
	PEM          string `json:"pem"`
}

// handleGetCA returns the panel's own CA certificate.
//
// Gated on node:read rather than served publicly. The CA certificate is not a
// secret -- its fingerprint is handed to unauthenticated agents at bootstrap,
// because pinning it is what makes enrolment safe against a hijacked DNS. What
// this route adds is the full certificate together with its subject and
// validity, which is fleet topology; there is no reason for an anonymous
// caller to enumerate it here when the bootstrap path already gives an
// enrolling agent exactly the one value it needs.
func (d Deps) handleGetCA(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}
	// Deps.CA is nil until the panel entrypoint builds one. Saying so beats a
	// panic inside the handler, and beats an empty certificate that reads as
	// "the CA has no subject".
	if d.CA == nil {
		WriteError(w, http.StatusServiceUnavailable, "unavailable", "CA not initialised")
		return
	}
	cert := d.CA.Certificate()
	WriteJSON(w, http.StatusOK, caCertDTO{
		Subject:      cert.Subject.String(),
		Issuer:       cert.Issuer.String(),
		NotBefore:    cert.NotBefore.Unix(),
		NotAfter:     cert.NotAfter.Unix(),
		SerialNumber: cert.SerialNumber.Text(16),
		Fingerprint:  d.CA.FingerprintSHA256(),
		PEM:          d.CA.CertPEM(),
	})
}

type nodeCertDTO struct {
	NodeID       int64  `json:"node_id"`
	NodeName     string `json:"node_name"`
	Subject      string `json:"subject"`
	NotAfter     int64  `json:"not_after"`
	SerialNumber string `json:"serial_number"`
	Fingerprint  string `json:"fingerprint"`
	EnrolledAt   int64  `json:"enrolled_at"`
	// Status is valid, expiring_soon, expired or unknown. "unknown" is a node
	// enrolled before the panel recorded expiry: the certificate works, and
	// nobody can say for how much longer.
	Status string `json:"status"`
	// DaysUntilExpiry is meaningless when Status is unknown; the client keys
	// off Status, not off a sentinel here.
	DaysUntilExpiry int `json:"days_until_expiry"`
}

type certStatsDTO struct {
	Total        int `json:"total"`
	Valid        int `json:"valid"`
	ExpiringSoon int `json:"expiring_soon"`
	Expired      int `json:"expired"`
	Unknown      int `json:"unknown"`
}

// handleListCertificates lists the certificates this caller's nodes hold.
//
// Scoped, like every other node listing: a reseller with node scopes sees the
// PKI state of their own nodes and learns nothing about the rest of the fleet.
// Only enrolled nodes appear -- a node with no fingerprint has no certificate,
// and listing it as one with an empty serial invites an operator to go looking
// for a certificate problem where the real state is "never enrolled".
func (d Deps) handleListCertificates(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}
	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	args := append([]any{}, store.ScopeArgs(rbac.ScopeOf(actor))...)
	rows, err := d.Store.Read().QueryContext(r.Context(),
		`SELECT id, name, cert_fingerprint, cert_not_after, cert_serial, enrolled_at
		   FROM nodes
		  WHERE cert_fingerprint IS NOT NULL
		    AND `+store.NodeScopeSQL+`
		  ORDER BY name`, args...)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list certificates")
		return
	}
	defer func() { _ = rows.Close() }()

	now := d.now()
	out := []nodeCertDTO{}
	var stats certStatsDTO
	for rows.Next() {
		var (
			id          int64
			name        string
			fingerprint string
			notAfter    sql.NullInt64
			serial      sql.NullString
			enrolledAt  sql.NullInt64
		)
		if err := rows.Scan(&id, &name, &fingerprint, &notAfter, &serial, &enrolledAt); err != nil {
			WriteError(w, http.StatusInternalServerError, "internal", "could not read certificates")
			return
		}
		cert := nodeCertDTO{
			NodeID:      id,
			NodeName:    name,
			Fingerprint: fingerprint,
			// The CN the panel signs is the node id; see CA.SignNodeCert.
			// Rendered here rather than stored, because it is derived.
			Subject:      "CN=" + nodes.NodeCommonName(id),
			SerialNumber: serial.String,
			EnrolledAt:   enrolledAt.Int64,
		}
		switch {
		case !notAfter.Valid:
			cert.Status = "unknown"
			stats.Unknown++
		default:
			cert.NotAfter = notAfter.Int64
			remaining := time.Unix(notAfter.Int64, 0).Sub(now)
			cert.DaysUntilExpiry = daysFrom(remaining)
			switch {
			case remaining <= 0:
				cert.Status = "expired"
				stats.Expired++
			case remaining <= certExpiryWarning:
				cert.Status = "expiring_soon"
				stats.ExpiringSoon++
			default:
				cert.Status = "valid"
				stats.Valid++
			}
		}
		stats.Total++
		out = append(out, cert)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not read certificates")
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"certificates": out, "stats": stats})
}

// daysFrom rounds a remaining duration to whole days, toward zero for the
// future and away from zero for the past.
//
// Truncation alone would report a certificate with 23 hours left as "0 days",
// which reads as "expires today" when it expires tomorrow; and one that lapsed
// an hour ago as "0 days" too, which reads as "still fine". Ceil for the
// future and floor for the past keeps both honest.
func daysFrom(remaining time.Duration) int {
	days := remaining.Hours() / 24
	if remaining < 0 {
		return int(math.Floor(days))
	}
	return int(math.Ceil(days))
}

// handleRevokeNodeCertificate withdraws a node's certificate.
//
// Revocation here is removal from the allow-list, not a CRL: control.VerifyPeer
// authenticates every agent by looking its presented fingerprint up in
// nodes.cert_fingerprint, so clearing that column locks the node out on its
// very next connection. Nothing has to be published and no agent has to be
// told; there is no window in which a revoked certificate still works.
//
// The node returns to 'pending' with enrolled_at cleared, because that is
// exactly what it now is: a node the panel knows about that has no identity.
// Getting back in means enrolling again with a fresh token, which is the
// point -- revocation is for a host whose key may have been copied, and the
// operator has to be sure the machine that returns is one they authorised.
//
// Gated on node:enroll rather than node:write. Issuing an identity and
// destroying one are the same capability over the same thing, and an operator
// trusted to edit a node's address is not automatically trusted to evict it
// from the control plane.
func (d Deps) handleRevokeNodeCertificate(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()
	var fingerprint string
	err = d.Store.Write(ctx, func(tx *sql.Tx) error {
		// Read the outgoing fingerprint inside the same transaction that
		// clears it, so the audit row names the certificate that was actually
		// revoked rather than whatever was there a moment earlier.
		err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(cert_fingerprint,'') FROM nodes WHERE id = ?`, id).Scan(&fingerprint)
		if err != nil {
			return err
		}
		if fingerprint == "" {
			return errNoCertificate
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE nodes
			    SET cert_fingerprint = NULL, cert_not_after = NULL, cert_serial = NULL,
			        enrolled_at = NULL, status = 'pending'
			  WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return audit.InTx(ctx, tx, RequestID(ctx), d.actorAudit(actor, r), audit.Record{
			Action:     "node.certificate.revoke",
			TargetType: "node",
			TargetID:   sql.NullInt64{Int64: id, Valid: true},
			Before:     map[string]any{"cert_fingerprint": fingerprint},
			Result:     "ok",
		})
	})
	switch {
	case errors.Is(err, errNoCertificate):
		// Not an error state worth a 500: the node simply has nothing to
		// revoke, and saying so is more useful than a generic failure.
		WriteError(w, http.StatusConflict, "no_certificate",
			"this node has no certificate to revoke")
		return
	case errors.Is(err, sql.ErrNoRows):
		WriteError(w, http.StatusNotFound, "not_found", "node not found")
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "internal", "could not revoke certificate")
		return
	}

	// The node is locked out already; the reconciler learns of it when the
	// agent's next connection is refused.
	WriteJSON(w, http.StatusOK, map[string]any{
		"revoked": true,
		"status":  "pending",
	})
}

// errNoCertificate distinguishes "nothing to revoke" from a database failure
// inside the write callback, where the only channel back is an error.
var errNoCertificate = errNoCert{}

type errNoCert struct{}

func (errNoCert) Error() string { return "node has no certificate" }
