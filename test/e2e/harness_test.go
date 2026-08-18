//go:build e2e

// Package e2e runs the SP1 definition of done against a real panel and a real
// agent talking over loopback with genuine mTLS.
//
// The plan's original harness drove Docker containers. Docker is not available
// on every machine that needs to run this, and the containers added nothing the
// in-process form cannot show: the agent binary and the agent package are the
// same code, the gRPC transport is real either way, and the certificates are
// the ones the panel actually issues. What this form gains is that it runs
// under `go test` in CI with no daemon, and a failure points at a line rather
// than a container log.
//
// What it deliberately does NOT cover, and which therefore remains verified
// only by install_test.sh and by hand: install.sh's platform guards, systemd
// unit installation, and the download of a real binary over the network.
package e2e

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/amyrm/antimage/internal/node/adapter/stub"
	"github.com/amyrm/antimage/internal/node/agent"
	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/httpapi"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	pb "github.com/amyrm/antimage/internal/shared/proto/antimage/v1"
	"github.com/amyrm/antimage/internal/shared/secrets"

	"database/sql"
)

type env struct {
	t         *testing.T
	store     *store.Store
	ca        *nodes.CA
	hub       *control.Hub
	http      *httptest.Server
	grpcAddr  string
	grpcSrv   *grpc.Server
	adminTok  string
	nodeID    int64
	stateDir  string
	agentRoot string

	agentCancel context.CancelFunc
	agentDone   chan struct{}
	agentCert   tls.Certificate
	agentCADER  []byte
	agentCfg    *agent.Config
}

func now() time.Time { return time.Now().UTC() }

// startPanel brings up the full panel: store, CA, mTLS gRPC listener, HTTP API,
// and the offline sweeper — the same wiring cmd/antimage-panel performs.
func startPanel(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()

	key := make([]byte, secrets.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "antimage.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ca, err := nodes.LoadOrCreateCA(context.Background(), st, box)
	if err != nil {
		t.Fatalf("LoadOrCreateCA: %v", err)
	}
	hub := control.NewHub()

	// Real mTLS, exactly as main.go configures it.
	serverCert, err := ca.IssueServerCert([]string{"localhost", "127.0.0.1"}, now())
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{serverCert},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    ca.ClientCAPool(),
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	deps := control.Deps{Store: st, CA: ca, Hub: hub, Now: now}
	grpcSrv := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterEnrollmentServer(grpcSrv, control.NewEnrollmentService(deps))
	pb.RegisterControlServer(grpcSrv, control.NewControlService(deps))
	go func() { _ = grpcSrv.Serve(ln) }()
	t.Cleanup(grpcSrv.Stop)

	api := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		Store:       st,
		Sessions:    auth.NewSessions(st, now),
		Limiter:     auth.NewLimiter(st, now),
		Hub:         hub,
		CA:          ca,
		Box:         box,
		DownloadDir: filepath.Join(dir, "downloads"),
		Now:         now,
	}))
	t.Cleanup(api.Close)

	// The offline sweeper, on a short interval so test 3 does not wait minutes.
	sweepCtx, stopSweep := context.WithCancel(context.Background())
	t.Cleanup(stopSweep)
	go nodes.NewSweeper(st, now).WithThreshold(3*time.Second).Run(sweepCtx, 250*time.Millisecond)

	e := &env{
		t: t, store: st, ca: ca, hub: hub, http: api,
		grpcAddr: ln.Addr().String(), grpcSrv: grpcSrv,
		stateDir: filepath.Join(dir, "node-state"), agentRoot: filepath.Join(dir, "node-services"),
	}
	if err := os.MkdirAll(e.agentRoot, 0o700); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	e.seedAdmin()
	return e
}

func (e *env) seedAdmin() {
	e.t.Helper()
	hash, err := auth.HashPassword("acceptance-pw")
	if err != nil {
		e.t.Fatalf("HashPassword: %v", err)
	}
	perms, err := json.Marshal(rbac.BuiltinRoles()["super_admin"])
	if err != nil {
		e.t.Fatalf("marshal perms: %v", err)
	}
	err = e.store.Write(context.Background(), func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`INSERT INTO roles (name, is_builtin, permissions) VALUES ('super_admin', 1, ?)`,
			string(perms))
		if err != nil {
			return err
		}
		roleID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(
			`INSERT INTO admins (username, password_hash, role_id, created_at) VALUES ('root',?,?,?)`,
			hash, roleID, now().Unix())
		return err
	})
	if err != nil {
		e.t.Fatalf("seed admin: %v", err)
	}

	res := e.api("POST", "/api/v1/auth/login", `{"username":"root","password":"acceptance-pw"}`)
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		e.t.Fatalf("login: %d", res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == auth.CookieName {
			e.adminTok = c.Value
		}
	}
	if e.adminTok == "" {
		e.t.Fatal("no session cookie")
	}
}

func (e *env) api(method, path, body string) *http.Response {
	e.t.Helper()
	var rdr *strings.Reader
	if body == "" {
		rdr = strings.NewReader("")
	} else {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.http.URL+path, rdr)
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", e.http.URL)
	if e.adminTok != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: e.adminTok})
	}
	res, err := e.http.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

func (e *env) apiJSON(method, path, body string, out any) int {
	e.t.Helper()
	res := e.api(method, path, body)
	defer func() { _ = res.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

// createNodeAndEnroll performs the operator half of the bootstrap: create the
// node, mint a single-use token, then run the REAL agent enrolment against the
// real mTLS listener.
func (e *env) createNodeAndEnroll() {
	e.t.Helper()

	var created struct {
		ID int64 `json:"id"`
	}
	if code := e.apiJSON("POST", "/api/v1/nodes",
		`{"name":"acceptance-1","address":"127.0.0.1"}`, &created); code != http.StatusCreated {
		e.t.Fatalf("create node: %d", code)
	}
	e.nodeID = created.ID

	var tok struct {
		Token   string `json:"token"`
		Command string `json:"command"`
	}
	if code := e.apiJSON("POST",
		fmt.Sprintf("/api/v1/nodes/%d/enroll-token", e.nodeID), "", &tok); code != http.StatusCreated {
		e.t.Fatalf("enroll token: %d", code)
	}
	if tok.Token == "" || tok.Command == "" {
		e.t.Fatalf("incomplete enroll-token response: %+v", tok)
	}

	sum := sha256.Sum256(e.ca.CertDER())
	e.agentCfg = &agent.Config{
		PanelURL:      e.grpcAddr,
		Token:         tok.Token,
		CAFingerprint: hex.EncodeToString(sum[:]),
		StateDir:      e.stateDir,
	}

	cert, caDER, nodeID, err := agent.Enroll(context.Background(), e.agentCfg)
	if err != nil {
		e.t.Fatalf("agent enrolment over mTLS failed: %v", err)
	}
	if nodeID != e.nodeID {
		e.t.Fatalf("agent enrolled as node %d, want %d", nodeID, e.nodeID)
	}
	e.agentCert, e.agentCADER = cert, caDER
	e.agentCfg.NodeID = nodeID
	e.agentCfg.Token = "" // single use, now spent
}

func (e *env) startAgent() {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	client := agent.NewClient(e.agentCfg, stub.New(e.agentRoot), agent.SystemClock{},
		e.agentCert, e.agentCADER)
	go func() {
		defer close(done)
		_ = client.Run(ctx)
	}()
	e.agentCancel, e.agentDone = cancel, done
	e.t.Cleanup(func() {
		if e.agentCancel != nil {
			e.agentCancel()
		}
	})
}

func (e *env) stopAgent() {
	e.t.Helper()
	if e.agentCancel == nil {
		return
	}
	e.agentCancel()
	select {
	case <-e.agentDone:
	case <-time.After(10 * time.Second):
		e.t.Fatal("agent did not stop")
	}
	e.agentCancel = nil
}

// --- polling helpers: each reports the last value it saw, so a timeout says
// what the state actually was rather than only that it was wrong. ---

func (e *env) waitFor(what string, d time.Duration, probe func() (string, bool)) {
	e.t.Helper()
	deadline := time.Now().Add(d)
	var last string
	for time.Now().Before(deadline) {
		v, ok := probe()
		last = v
		if ok {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s after %s; last observed: %s", what, d, last)
}

func (e *env) nodeStatus() string {
	var s string
	_ = e.store.Read().QueryRow(`SELECT status FROM nodes WHERE id = ?`, e.nodeID).Scan(&s)
	return s
}

func (e *env) revisions() (desired, applied int64) {
	_ = e.store.Read().QueryRow(
		`SELECT desired_revision, applied_revision FROM nodes WHERE id = ?`, e.nodeID).
		Scan(&desired, &applied)
	return
}

func (e *env) waitForStatus(want string, d time.Duration) {
	e.t.Helper()
	e.waitFor("status "+want, d, func() (string, bool) {
		got := e.nodeStatus()
		return "status=" + got, got == want
	})
}

func (e *env) waitForConverged(d time.Duration) {
	e.t.Helper()
	e.waitFor("applied_revision to catch up", d, func() (string, bool) {
		des, app := e.revisions()
		return fmt.Sprintf("desired=%d applied=%d", des, app), des == app && des > 0
	})
}

func (e *env) createService(body string) {
	e.t.Helper()
	if code := e.apiJSON("POST",
		fmt.Sprintf("/api/v1/nodes/%d/services", e.nodeID), body, nil); code != http.StatusCreated {
		e.t.Fatalf("create service: %d", code)
	}
}

func (e *env) managedFiles() map[string]string {
	e.t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(e.agentRoot)
	if err != nil {
		return out
	}
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(e.agentRoot, en.Name()))
		if err == nil {
			out[en.Name()] = string(b)
		}
	}
	return out
}

func (e *env) managedFilesContain(needle string) bool {
	for _, body := range e.managedFiles() {
		if strings.Contains(body, needle) {
			return true
		}
	}
	return false
}

func (e *env) applyRunCount() int {
	var n int
	_ = e.store.Read().QueryRow(
		`SELECT count(*) FROM node_apply_runs WHERE node_id = ?`, e.nodeID).Scan(&n)
	return n
}

// corruptManagedFile simulates an operator hand-editing the stub's managed
// file. The stub adapter checksums its payload, so this is exactly the drift
// the Observe/Plan cycle exists to detect.
func (e *env) corruptManagedFile(name string) {
	e.t.Helper()
	path := filepath.Join(e.agentRoot, name)
	body, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("read managed file: %v", err)
	}
	if err := os.WriteFile(path,
		append(body, []byte("\n# HAND-EDITED-BY-AN-OPERATOR\n")...), 0o600); err != nil {
		e.t.Fatalf("corrupt managed file: %v", err)
	}
}

// dialControlStreamOnce opens one control stream with the agent's issued
// certificate and sends Hello, returning the first error. After the node row is
// deleted its fingerprint is no longer on the allow-list, so VerifyPeer must
// refuse.
func (e *env) dialControlStreamOnce() error {
	pool := x509.NewCertPool()
	caCert, err := x509.ParseCertificate(e.agentCADER)
	if err != nil {
		return err
	}
	pool.AddCert(caCert)

	conn, err := grpc.NewClient(e.grpcAddr, grpc.WithTransportCredentials(
		credentials.NewTLS(&tls.Config{
			Certificates: []tls.Certificate{e.agentCert},
			RootCAs:      pool,
			MinVersion:   tls.VersionTLS13,
		})))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stream, err := pb.NewControlClient(conn).Stream(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.AgentMessage{Payload: &pb.AgentMessage_Hello{
		Hello: &pb.Hello{NodeId: e.nodeID, ProtocolVersion: 1},
	}}); err != nil {
		return err
	}
	_, err = stream.Recv()
	return err
}

// runInRepoRoot executes a gate from the repository root.
func runInRepoRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = filepath.Join(wd, "..", "..")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
