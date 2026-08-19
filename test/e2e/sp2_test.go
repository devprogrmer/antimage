//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
	"github.com/amyrm/antimage/internal/node/adapter/singbox"
	"github.com/amyrm/antimage/internal/node/adapter/xray"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/subjects"
)

// sp2Runtime records what the adapter asked of the proxy, so the lifecycle
// test can assert that a hot user add did not restart it.
type sp2Runtime struct {
	restarts int
	added    []string
	healthy  bool
	failNext error
}

func newSP2Runtime() *sp2Runtime { return &sp2Runtime{healthy: true} }

func (r *sp2Runtime) Available(context.Context) error { return nil }
func (r *sp2Runtime) AddUser(_ context.Context, tag string, u xray.User, _ xray.Protocol) error {
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	r.added = append(r.added, tag+"/"+u.Email)
	return nil
}
func (r *sp2Runtime) RemoveUser(context.Context, string, string) error { return nil }
func (r *sp2Runtime) Reload(context.Context) error                     { return nil }
func (r *sp2Runtime) Restart(context.Context) error {
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	r.restarts++
	return nil
}
func (r *sp2Runtime) Healthy(context.Context) (bool, string) {
	if r.healthy {
		return true, "active"
	}
	return false, "inactive (dead)"
}

// singboxRuntime is the restart-only counterpart.
type singboxRuntime struct{ restarts int }

func (r *singboxRuntime) Available(context.Context) error { return nil }
func (r *singboxRuntime) Restart(context.Context) error   { r.restarts++; return nil }
func (r *singboxRuntime) Healthy(context.Context) (bool, string) {
	return true, "active"
}

// sp2Env extends the SP1 harness with a subject store and a real adapter, so
// the whole path from an operator's HTTP call to bytes in a proxy config is
// exercised end to end.
type sp2Env struct {
	*env
	serviceID int64
	adapter   adapter.Adapter
	rt        *sp2Runtime
	sbRT      *singboxRuntime
	confDir   string
}

// restartCount reports how many times the proxy process has been restarted,
// whichever adapter is under test. Kept as one accessor so a test asserting a
// disruption property reads the same way for both runtimes.
func (s *sp2Env) restartCount() int {
	if s.rt != nil {
		return s.rt.restarts
	}
	return s.sbRT.restarts
}

func startSP2(t *testing.T, adapterKind string) *sp2Env {
	t.Helper()
	return startSP2WithPort(t, adapterKind, 8443)
}

// startSP2WithPort is startSP2 with the inbound's listen port chosen by the
// caller. The real-runtime tests need a port that is actually free, because
// something really binds it.
func startSP2WithPort(t *testing.T, adapterKind string, port int) *sp2Env {
	t.Helper()
	e := startPanel(t)
	e.createNodeAndEnroll()

	confDir := filepath.Join(t.TempDir(), "conf")
	s := &sp2Env{env: e, confDir: confDir}

	switch adapterKind {
	case "xray":
		s.rt = newSP2Runtime()
		s.adapter = xray.New(confDir, s.rt, true) // hot add supported
	case "singbox":
		s.sbRT = &singboxRuntime{}
		s.adapter = singbox.New(confDir, s.sbRT)
	default:
		t.Fatalf("unknown adapter kind %q", adapterKind)
	}

	// Create the inbound through the API, exactly as an operator would.
	params := fmt.Sprintf(
		`{"protocol":"vless","port":%d,"security":"tls","cert_file":"/c","key_file":"/k"}`, port)
	if adapterKind == "singbox" {
		params = fmt.Sprintf(
			`{"protocol":"vless","port":%d,"tls":true,"cert_file":"/c","key_file":"/k"}`, port)
	}
	// A real proxy will not start with certificates that do not exist, so the
	// real-runtime tests ask for a plain inbound. Everything else about the
	// document, the plan and the generated config is identical.
	if os.Getenv("ANTIMAGE_REALRUNTIME") == "1" {
		params = fmt.Sprintf(`{"protocol":"vless","port":%d,"security":"none"}`, port)
		if adapterKind == "singbox" {
			params = fmt.Sprintf(`{"protocol":"vless","port":%d}`, port)
		}
	}
	var created struct {
		ID int64 `json:"id"`
	}
	code := s.apiJSON("POST", fmt.Sprintf("/api/v1/nodes/%d/services", s.nodeID),
		fmt.Sprintf(`{"adapter_kind":%q,"params":%s}`, adapterKind, params), &created)
	if code != http.StatusCreated {
		t.Fatalf("create service: %d", code)
	}
	s.serviceID = created.ID
	return s
}

// createSubject goes through the HTTP API and returns the subject id.
func (s *sp2Env) createSubject(t *testing.T, name string, expiresAt *int64) int64 {
	t.Helper()
	body := map[string]any{"name": name, "service_ids": []int64{s.serviceID}}
	if expiresAt != nil {
		body["expires_at"] = *expiresAt
	}
	raw, _ := json.Marshal(body)

	var out struct {
		ID int64 `json:"id"`
	}
	if code := s.apiJSON("POST", "/api/v1/subjects", string(raw), &out); code != http.StatusCreated {
		t.Fatalf("create subject %s: %d", name, code)
	}
	return out.ID
}

// snapshot builds the desired document the agent would fetch.
func (s *sp2Env) snapshot(t *testing.T) *nodes.Snapshot {
	t.Helper()
	var snap *nodes.Snapshot
	err := s.store.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		snap, err = nodes.BuildDesiredSnapshot(context.Background(), tx, s.nodeID,
			nodes.WithUnsealer(s.box()))
		return err
	})
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	return snap
}

// reconcile runs the real adapter over the real document.
func (s *sp2Env) reconcile(t *testing.T) adapter.Plan {
	t.Helper()
	snap := s.snapshot(t)

	var desired adapter.Desired
	if err := json.Unmarshal(snap.Bytes, &desired); err != nil {
		t.Fatalf("decode desired document: %v", err)
	}

	ctx := context.Background()
	obs, err := s.adapter.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	plan, err := s.adapter.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, step := range plan.Steps {
		if _, err := s.adapter.Apply(ctx, step); err != nil {
			t.Fatalf("Apply %s: %v", step.Kind, err)
		}
	}
	return plan
}

func (s *sp2Env) generatedConfig(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(s.confDir,
		fmt.Sprintf("antimage-%d.json", s.serviceID)))
	if err != nil {
		return ""
	}
	return string(body)
}

func (s *sp2Env) credential(t *testing.T, subjectID int64) string {
	t.Helper()
	var out struct {
		Value string `json:"value"`
	}
	code := s.apiJSON("GET",
		fmt.Sprintf("/api/v1/subjects/%d/credentials/uuid", subjectID), "", &out)
	if code != http.StatusOK {
		t.Fatalf("reveal credential: %d", code)
	}
	return out.Value
}

// TestSP2Lifecycle is the mandatory end-to-end proof, run against BOTH
// adapters: create a user, assign them, generate credentials, build desired
// state, verify the revision and hash move, plan, apply, and confirm the
// credential reaches the generated proxy configuration.
func TestSP2Lifecycle(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		t.Run(kind, func(t *testing.T) {
			e := startSP2(t, kind)

			var revBefore int64
			_ = e.store.Read().QueryRow(
				`SELECT desired_revision FROM nodes WHERE id = ?`, e.nodeID).Scan(&revBefore)
			hashBefore := e.snapshot(t).SHA256

			// 1. create + assign + credentials
			id := e.createSubject(t, "alice", nil)
			cred := e.credential(t, id)
			if cred == "" {
				t.Fatal("no credential was generated")
			}

			// 2. desired state, revision and hash both move
			snap := e.snapshot(t)
			if snap.Revision <= revBefore {
				t.Fatalf("revision did not move: %d -> %d", revBefore, snap.Revision)
			}
			if snap.SHA256 == hashBefore {
				t.Fatal("the document hash did not change when a subject was added")
			}
			if len(snap.Document.Subjects) != 1 {
				t.Fatalf("document carries %d subjects, want 1", len(snap.Document.Subjects))
			}

			// 3. plan + apply, and the credential reaches the proxy config
			e.reconcile(t)
			config := e.generatedConfig(t)
			if !strings.Contains(config, cred) {
				t.Fatalf("the subject's credential never reached the %s config:\n%s", kind, config)
			}

			// 4. probe reports healthy
			h, err := e.adapter.Probe(context.Background())
			if err != nil || !h.OK {
				t.Fatalf("Probe = %+v, err %v", h, err)
			}

			// 5. converged: re-planning produces nothing
			if plan := e.reconcile(t); !plan.IsEmpty() {
				t.Fatalf("not converged; still plans %d step(s)", len(plan.Steps))
			}

			// --- UPDATE: a second user appears, the first is untouched ---
			second := e.createSubject(t, "bob", nil)
			secondCred := e.credential(t, second)
			e.reconcile(t)
			config = e.generatedConfig(t)
			if !strings.Contains(config, cred) || !strings.Contains(config, secondCred) {
				t.Fatal("after adding a second user the config does not carry both")
			}

			// --- DISABLE: the user disappears from the running config ---
			if code := e.apiJSON("PUT", fmt.Sprintf("/api/v1/subjects/%d", second),
				`{"enabled":false}`, nil); code != http.StatusNoContent {
				t.Fatalf("disable: %d", code)
			}
			e.reconcile(t)
			if strings.Contains(e.generatedConfig(t), secondCred) {
				t.Fatal("a disabled user is still in the generated config")
			}
			if !strings.Contains(e.generatedConfig(t), cred) {
				t.Fatal("disabling one user removed another")
			}

			// --- DELETE: credentials and grants go, node converges ---
			if code := e.apiJSON("DELETE", fmt.Sprintf("/api/v1/subjects/%d", id), "", nil); code != http.StatusNoContent {
				t.Fatalf("delete: %d", code)
			}
			e.reconcile(t)
			if strings.Contains(e.generatedConfig(t), cred) {
				t.Fatal("a deleted user is still in the generated config")
			}
			var creds int
			_ = e.store.Read().QueryRow(
				`SELECT count(*) FROM subject_credentials WHERE subject_id = ?`, id).Scan(&creds)
			if creds != 0 {
				t.Errorf("%d credential rows survived the delete", creds)
			}
			if plan := e.reconcile(t); !plan.IsEmpty() {
				t.Fatalf("not converged after delete: %+v", plan.Steps)
			}
		})
	}
}

// Expiry, end to end: the sweeper retires the subject, the document drops them,
// and the node converges to a config that no longer carries their credential.
func TestSP2ExpiryReachesTheProxyConfig(t *testing.T) {
	e := startSP2(t, "xray")

	expiry := time.Now().UTC().Add(2 * time.Hour).Unix()
	id := e.createSubject(t, "temporary", &expiry)
	cred := e.credential(t, id)

	e.reconcile(t)
	if !strings.Contains(e.generatedConfig(t), cred) {
		t.Fatal("the subject never reached the config before expiry")
	}

	sw := subjects.NewSweeper(e.store,
		func() time.Time { return time.Unix(expiry, 0).UTC().Add(time.Minute) },
		func(int64, int64) {}, nodes.WithUnsealer(e.box()))
	n, err := sw.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d subjects, want 1", n)
	}

	e.reconcile(t)
	if strings.Contains(e.generatedConfig(t), cred) {
		t.Fatal("an expired subject's credential is still in the running config")
	}
	if plan := e.reconcile(t); !plan.IsEmpty() {
		t.Fatalf("not converged after expiry: %+v", plan.Steps)
	}
}

// Disruption, end to end: on Xray adding a user must not restart the proxy.
func TestSP2HotAddDoesNotRestartXray(t *testing.T) {
	e := startSP2(t, "xray")

	e.createSubject(t, "first", nil)
	e.reconcile(t)
	restartsAfterFirst := e.rt.restarts

	e.createSubject(t, "second", nil)
	plan := e.reconcile(t)

	if e.rt.restarts != restartsAfterFirst {
		t.Errorf("adding a user restarted Xray: %d -> %d", restartsAfterFirst, e.rt.restarts)
	}
	if len(e.rt.added) == 0 {
		t.Error("the user was not added through the management API")
	}
	if plan.MaxDisruption() >= adapter.DisruptRestart {
		t.Errorf("hot add classified as %v", plan.MaxDisruption())
	}
}

// Recovery: a transient runtime failure must surface, and the next
// reconciliation must converge rather than believing it already succeeded.
func TestSP2RecoversFromATransientApplyFailure(t *testing.T) {
	e := startSP2(t, "xray")
	e.createSubject(t, "alice", nil)

	// Fail the first restart.
	e.rt.failNext = fmt.Errorf("systemctl: connection timed out")

	snap := e.snapshot(t)
	var desired adapter.Desired
	if err := json.Unmarshal(snap.Bytes, &desired); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ctx := context.Background()
	obs, _ := e.adapter.Observe(ctx)
	plan, err := e.adapter.Plan(ctx, desired, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var sawFailure bool
	for _, step := range plan.Steps {
		if _, err := e.adapter.Apply(ctx, step); err != nil {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatal("the injected failure did not surface")
	}

	// Recover.
	if plan := e.reconcile(t); plan.IsEmpty() {
		t.Fatal("nothing was re-planned after a failed apply")
	}
	if plan := e.reconcile(t); !plan.IsEmpty() {
		t.Fatalf("did not converge after recovery: %+v", plan.Steps)
	}
}

// Repeated reconciliation with unchanged desired state must do nothing: the
// agent reconciles on a timer, so a plan that never empties would rewrite
// config and restart the proxy forever.
func TestSP2RepeatedReconciliationIsANoOp(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		t.Run(kind, func(t *testing.T) {
			e := startSP2(t, kind)
			e.createSubject(t, "alice", nil)
			e.reconcile(t)

			for i := 0; i < 5; i++ {
				if plan := e.reconcile(t); !plan.IsEmpty() {
					t.Fatalf("reconcile %d planned %d step(s) with unchanged state: %+v",
						i, len(plan.Steps), plan.Steps)
				}
			}
		})
	}
}

// Revocation, end to end: removing a user must reach the RUNNING proxy, not
// just the config file on disk.
//
// The lifecycle test above proves the credential leaves the generated config.
// That is necessary but not sufficient: Xray goes on serving a user until it is
// told to stop, so a revocation that rewrote the file without restarting would
// pass every config assertion while the revoked user stayed connected. This
// test asserts the part that actually terminates access.
func TestSP2RevocationReachesTheRunningProxy(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		t.Run(kind, func(t *testing.T) {
			e := startSP2(t, kind)

			keep := e.createSubject(t, "alice", nil)
			keepCred := e.credential(t, keep)
			revoke := e.createSubject(t, "mallory", nil)
			revokeCred := e.credential(t, revoke)

			e.reconcile(t)
			if !strings.Contains(e.generatedConfig(t), revokeCred) {
				t.Fatal("precondition: the user to revoke never reached the config")
			}
			restartsBefore := e.restartCount()

			// Revoke by deleting the subject outright.
			if code := e.apiJSON("DELETE", fmt.Sprintf("/api/v1/subjects/%d", revoke), "", nil); //nolint:lll
			code != http.StatusNoContent {
				t.Fatalf("delete: %d", code)
			}
			plan := e.reconcile(t)

			config := e.generatedConfig(t)
			if strings.Contains(config, revokeCred) {
				t.Error("the revoked credential is still in the generated config")
			}
			if !strings.Contains(config, keepCred) {
				t.Error("revoking one user removed another")
			}

			// The part that matters: the proxy process learned about it.
			if plan.MaxDisruption() < adapter.DisruptRestart {
				t.Errorf("revocation classified as %v, want at least %v; the running "+
					"process would never be told", plan.MaxDisruption(), adapter.DisruptRestart)
			}
			if e.restartCount() == restartsBefore {
				t.Errorf("revocation did not restart the proxy (%d restarts throughout); "+
					"the revoked user keeps their session", e.restartCount())
			}

			if next := e.reconcile(t); !next.IsEmpty() {
				t.Errorf("not converged after revocation: %+v", next.Steps)
			}
		})
	}
}
