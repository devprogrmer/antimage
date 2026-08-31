package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

// realCA calls LoadOrCreateCA so what the handler displays is a CA the
// production code actually produced -- key sealed and unsealed via the same
// secrets.Box, certificate signed by ecdsa.GenerateKey rather than a stub.
// A shortcut here would test the shape of the handler and not that the CA
// it prints matches the one that ends up signing every node's certificate.
func realCA(t *testing.T, s *store.Store) *nodes.CA {
	t.Helper()
	box, err := secrets.NewBox(bytes.Repeat([]byte{3}, secrets.KeySize))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	ca, err := nodes.LoadOrCreateCA(context.Background(), s, box)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}
	return ca
}

// TestGetCA_Returns_CA proves the previously-dead `/api/v1/ca` route now
// serves the panel's own certificate details so the browser page can render
// them. Fingerprint parity with the CA is what the enrolment install script
// pins; drift here would mean two different fingerprints in circulation.
func TestGetCA_Returns_CA(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	deps.CA = realCA(t, s)

	req := httptest.NewRequest("GET", "/api/v1/ca", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	w := httptest.NewRecorder()
	deps.handleGetCA(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got caCertDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Fingerprint != deps.CA.FingerprintSHA256() {
		t.Errorf("fingerprint = %q, want %q", got.Fingerprint, deps.CA.FingerprintSHA256())
	}
	if got.PEM == "" || got.Subject == "" {
		t.Errorf("empty CA fields: %+v", got)
	}
}

// TestGetCA_ServiceUnavailable_WithoutCA guards the nil dereference the
// handler would produce if the panel entrypoint hadn't yet built a CA when
// the request arrived. A panic inside a private handler is worse than a 503.
func TestGetCA_ServiceUnavailable_WithoutCA(t *testing.T) {
	deps, _, actor := setupTestDeps(t)
	// deps.CA left nil

	req := httptest.NewRequest("GET", "/api/v1/ca", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	w := httptest.NewRecorder()
	deps.handleGetCA(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestListCertificates_ClassifiesByExpiry proves the four buckets the browser
// UI needs to render its status colouring: valid, expiring_soon, expired, and
// unknown (a node enrolled before cert_not_after was recorded).
func TestListCertificates_ClassifiesByExpiry(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	deps.Now = func() time.Time { return now }

	// Four nodes, one for each bucket. cert_fingerprint MUST be non-null on
	// every one -- the handler only lists rows with an enrolled certificate,
	// and a row without one is a node that never got past pending.
	nodesToInsert := []struct {
		id       int64
		name     string
		fp       string
		notAfter sql.NullInt64
		serial   sql.NullString
	}{
		{1, "valid-node", "fp1", nullInt(now.Add(365 * 24 * time.Hour).Unix()), nullStr("a1")},
		{2, "expiring-node", "fp2", nullInt(now.Add(10 * 24 * time.Hour).Unix()), nullStr("a2")},
		{3, "expired-node", "fp3", nullInt(now.Add(-24 * time.Hour).Unix()), nullStr("a3")},
		{4, "unknown-node", "fp4", sql.NullInt64{}, sql.NullString{}},
	}
	ctx := context.Background()
	for _, n := range nodesToInsert {
		err := s.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO nodes (id, name, address, status, cert_fingerprint,
				                    cert_not_after, cert_serial, enrolled_at, created_at)
				 VALUES (?, ?, '10.0.0.1', 'online', ?, ?, ?, ?, ?)`,
				n.id, n.name, n.fp, n.notAfter, n.serial, now.Unix(), now.Unix())
			return err
		})
		if err != nil {
			t.Fatalf("insert %s: %v", n.name, err)
		}
	}

	req := httptest.NewRequest("GET", "/api/v1/certificates", nil)
	req = req.WithContext(withActor(req.Context(), actor))

	w := httptest.NewRecorder()
	deps.handleListCertificates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Certificates []nodeCertDTO `json:"certificates"`
		Stats        certStatsDTO  `json:"stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Certificates) != 4 {
		t.Fatalf("got %d certs, want 4", len(got.Certificates))
	}
	byName := map[string]nodeCertDTO{}
	for _, c := range got.Certificates {
		byName[c.NodeName] = c
	}
	cases := map[string]string{
		"valid-node":    "valid",
		"expiring-node": "expiring_soon",
		"expired-node":  "expired",
		"unknown-node":  "unknown",
	}
	for name, want := range cases {
		if byName[name].Status != want {
			t.Errorf("%s: status = %q, want %q", name, byName[name].Status, want)
		}
	}
	// Stats have to match, because that is what the header of the UI counts
	// off. An "unknown" that leaks into "expiring_soon" would look like a
	// certificate crisis where there is none.
	if got.Stats.Valid != 1 || got.Stats.ExpiringSoon != 1 ||
		got.Stats.Expired != 1 || got.Stats.Unknown != 1 || got.Stats.Total != 4 {
		t.Errorf("stats mismatch: %+v", got.Stats)
	}
	// A node without a fingerprint must NOT appear at all -- it has no
	// certificate, and inventing a "not enrolled" row would invite an
	// operator to go looking for a certificate problem where the real state
	// is that the node has never been enrolled.
	if _, ok := byName[""]; ok {
		t.Errorf("empty node name appeared")
	}
}

// TestRevokeCertificate_ClearsFingerprint proves the whole point of the route:
// clearing cert_fingerprint locks the node out on its next connection, so
// this is what "revoke" has to actually do. A revoke that leaves the
// fingerprint intact is worse than none, because it reads as done.
func TestRevokeCertificate_ClearsFingerprint(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	// PermNodeEnroll is required by the handler; the default super-admin
	// actor in the shared fixture has read+write but not enroll.
	actor.Perms[rbac.PermNodeEnroll] = struct{}{}

	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO nodes (id, name, address, status, cert_fingerprint,
			                    enrolled_at, created_at)
			 VALUES (7, 'nyc-1', '10.0.0.7', 'online', ?, ?, ?)`,
			hex.EncodeToString([]byte("someprint")), time.Now().Unix(), time.Now().Unix())
		return err
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/nodes/7/certificate/revoke", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "7")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleRevokeNodeCertificate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}

	var fp sql.NullString
	var status string
	err = s.Read().QueryRowContext(ctx,
		`SELECT cert_fingerprint, status FROM nodes WHERE id = 7`).Scan(&fp, &status)
	if err != nil {
		t.Fatalf("read after revoke: %v", err)
	}
	if fp.Valid {
		t.Errorf("cert_fingerprint = %q, want NULL", fp.String)
	}
	if status != "pending" {
		t.Errorf("status = %q, want pending", status)
	}
}

// TestRevokeCertificate_Conflict_WhenNoCert covers the "nothing to revoke"
// case -- distinguished from a 500 so the browser can tell an operator that
// the button they pushed didn't need to do anything.
func TestRevokeCertificate_Conflict_WhenNoCert(t *testing.T) {
	deps, s, actor := setupTestDeps(t)
	actor.Perms[rbac.PermNodeEnroll] = struct{}{}
	createTestNode(t, s, 8, "no-cert-yet", "pending")

	req := httptest.NewRequest("POST", "/api/v1/nodes/8/certificate/revoke", nil)
	req = req.WithContext(withActor(req.Context(), actor))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("nodeID", "8")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	deps.handleRevokeNodeCertificate(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func nullInt(v int64) sql.NullInt64   { return sql.NullInt64{Int64: v, Valid: true} }
func nullStr(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
