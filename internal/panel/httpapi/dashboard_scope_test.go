package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/auth"
)

// The dashboard family reports aggregates, and an aggregate is a disclosure:
// "the fleet has 12 nodes" tells a tenant with one node about eleven they have
// no relationship to, and top-users names other tenants' subjects outright.
//
// Every test here seeds TWO tenants and asserts each sees only their own. That
// shape matters: a test with a single tenant passes just as happily against a
// handler that ignores scope entirely, because with one owner the scoped and
// unscoped answers are identical. Two tenants is the smallest world in which
// the difference is observable.

// grantNodeScope gives an admin an explicit node scope row, which is what makes
// a node visible to a non-super caller.
func grantNodeScope(t *testing.T, env *testEnv, username string, nodeID int64) {
	t.Helper()
	err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		var adminID int64
		if err := tx.QueryRow(
			`SELECT id FROM admins WHERE username = ?`, username).Scan(&adminID); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO admin_scopes (admin_id, scope_type, scope_id) VALUES (?,'node',?)`,
			adminID, nodeID)
		return err
	})
	if err != nil {
		t.Fatalf("grant node scope to %s: %v", username, err)
	}
}

// recordUsage writes an hourly rollup, which is what the traffic figures read.
func recordUsage(t *testing.T, env *testEnv, subjectID, up, down int64) {
	t.Helper()
	hour := time.Now().UTC().Truncate(time.Hour).Unix()
	err := env.store.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO usage_rollups_hourly
			   (subject_id, hour_start, uplink_bytes, downlink_bytes)
			 VALUES (?,?,?,?)`, subjectID, hour, up, down)
		return err
	})
	if err != nil {
		t.Fatalf("record usage for subject %d: %v", subjectID, err)
	}
}

// streamOnce opens the SSE stream, takes the first metrics frame, and hangs up.
//
// The handler blocks on a five second ticker, so the request context is
// cancelled once the opening snapshot -- which is written synchronously, before
// the ticker starts -- is on the wire.
func streamOnce(t *testing.T, env *testEnv, token string) (int, DashboardMetrics) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stream", nil).WithContext(ctx)
	req.Header.Set("Origin", "https://panel.local")
	req.Host = "panel.local"
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	}

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.ServeHTTP(rec, req)
	}()

	// The opening frame is synchronous; this only has to outlast the queries.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	var m DashboardMetrics
	if _, data, found := strings.Cut(body, "event: metrics\ndata: "); found {
		frame, _, _ := strings.Cut(data, "\n\n")
		if err := json.Unmarshal([]byte(frame), &m); err != nil {
			t.Fatalf("metrics frame is not JSON: %v (frame %q)", err, frame)
		}
	}
	return rec.Code, m
}

// The endpoint shipped querying five columns that do not exist, so it failed on
// its second query and emitted an empty 200 to everyone, forever. No test
// noticed, because nothing asserted that anything came out of it.
//
// This is that assertion. It fails against a stream that emits nothing, which
// is what any future rename of these columns would produce.
func TestDashboardStreamActuallyEmitsMetrics(t *testing.T) {
	// newSubjectEnv already seeds one node, so this makes two.
	env, adminToken, svcID := newSubjectEnv(t)
	_, subjectID := seedTenant(t, env, "alice", svcID, adminToken)
	nodeID := env.seedNode(t, "edge-1")
	recordUsage(t, env, subjectID, 1<<30, 1<<30)

	code, m := streamOnce(t, env, adminToken)
	if code != http.StatusOK {
		t.Fatalf("stream = %d, want 200", code)
	}
	if m.Timestamp == 0 {
		t.Fatal("the stream produced no metrics frame at all; it is querying " +
			"columns that do not exist and giving up before the first send")
	}
	if m.TotalSubjects != 1 {
		t.Errorf("total_subjects = %d, want 1", m.TotalSubjects)
	}
	if m.NodesTotal != 2 {
		t.Errorf("nodes_total = %d, want 2", m.NodesTotal)
	}
	if m.TrafficTodayGB < 1.9 || m.TrafficTodayGB > 2.1 {
		t.Errorf("traffic_today_gb = %v, want ~2 (from the hourly rollups, "+
			"not a lifetime quota counter)", m.TrafficTodayGB)
	}
	// A node that has never reported has a NULL last_seen_at. It must still be
	// listed, as offline, rather than dropped from the dashboard entirely.
	var found bool
	for _, n := range m.Nodes {
		if n.ID == nodeID {
			found = true
			if n.Status != "offline" {
				t.Errorf("a node that never reported has status %q, want offline", n.Status)
			}
		}
	}
	if !found {
		t.Errorf("nodes = %+v, want the never-reported node to appear", m.Nodes)
	}
}

func TestDashboardStreamIsScopedToTheCaller(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, aliceSubject := seedTenant(t, env, "alice", svcID, adminToken)
	_, bobSubject := seedTenant(t, env, "bob", svcID, adminToken)

	aliceNode := env.seedNode(t, "alice-edge")
	env.seedNode(t, "bob-edge")
	grantNodeScope(t, env, "alice", aliceNode)

	recordUsage(t, env, aliceSubject, 1<<30, 0)
	recordUsage(t, env, bobSubject, 50<<30, 0)

	code, m := streamOnce(t, env, aliceToken)
	if code != http.StatusOK {
		t.Fatalf("stream = %d, want 200", code)
	}
	if m.Timestamp == 0 {
		t.Fatal("no metrics frame; the scoping assertions below would prove nothing")
	}

	if m.TotalSubjects != 1 {
		t.Errorf("alice's stream reports total_subjects = %d, want 1; she is "+
			"being told how many customers the whole platform has", m.TotalSubjects)
	}
	if m.NodesTotal != 1 {
		t.Errorf("alice's stream reports nodes_total = %d, want 1; she is being "+
			"told the size of a fleet she has one node in", m.NodesTotal)
	}
	if len(m.Nodes) != 1 || m.Nodes[0].ID != aliceNode {
		t.Errorf("alice's stream lists %+v, want only her own node", m.Nodes)
	}
	// Bob's 50 GiB must not appear in alice's total.
	if m.TrafficTodayGB > 1.1 {
		t.Errorf("traffic_today_gb = %v for alice, want ~1; another tenant's "+
			"traffic is being summed into her dashboard", m.TrafficTodayGB)
	}
}

func TestDashboardStreamRequiresAuthentication(t *testing.T) {
	env, _, _ := newSubjectEnv(t)
	env.seedNode(t, "edge-1")

	code, m := streamOnce(t, env, "")
	if code == http.StatusOK {
		t.Errorf("an unauthenticated request opened the metrics stream (%d)", code)
	}
	if m.Timestamp != 0 {
		t.Errorf("an unauthenticated caller received metrics: %+v", m)
	}
}

// A wildcard CORS header on a cookie-authenticated stream lets any origin read
// one tenant's live figures out of their browser.
func TestDashboardStreamDoesNotAllowAnyOrigin(t *testing.T) {
	env, adminToken, _ := newSubjectEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/stream", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Host = "panel.local"
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: adminToken})

	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		env.handler.ServeHTTP(rec, req)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got == "*" {
		t.Error("the metrics stream answers Access-Control-Allow-Origin: *, so " +
			"any site the operator visits can read their dashboard")
	}
}

// ---------------------------------------------------------------- overview

func TestDashboardOverviewIsScopedToTheCaller(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	seedTenant(t, env, "bob", svcID, adminToken)

	aliceNode := env.seedNode(t, "alice-edge")
	env.seedNode(t, "bob-edge")
	grantNodeScope(t, env, "alice", aliceNode)

	res := env.get(t, "/api/v1/dashboard/overview", aliceToken)
	if res.Code != http.StatusOK {
		t.Fatalf("overview = %d: %s", res.Code, res.Body)
	}
	var out struct {
		Nodes    struct{ Total int } `json:"nodes"`
		Subjects struct{ Total int } `json:"subjects"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Nodes.Total != 1 {
		t.Errorf("alice's overview reports %d nodes, want 1", out.Nodes.Total)
	}
	if out.Subjects.Total != 1 {
		t.Errorf("alice's overview reports %d subjects, want 1", out.Subjects.Total)
	}
}

// The overview is cached per admin. A cache keyed by admin but filled with
// global figures is exactly what was there before, so this asserts the two
// tenants get DIFFERENT answers -- the property a shared cache would break.
func TestDashboardOverviewCacheDoesNotServeOneTenantAnothersFigures(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, _ := seedTenant(t, env, "alice", svcID, adminToken)
	bobToken, _ := seedTenant(t, env, "bob", svcID, adminToken)

	aliceNode := env.seedNode(t, "alice-edge")
	grantNodeScope(t, env, "alice", aliceNode)

	total := func(token string) int {
		res := env.get(t, "/api/v1/dashboard/overview", token)
		if res.Code != http.StatusOK {
			t.Fatalf("overview = %d: %s", res.Code, res.Body)
		}
		var out struct {
			Nodes struct{ Total int } `json:"nodes"`
		}
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Nodes.Total
	}

	// Alice first, so hers is the entry in the cache when bob asks.
	if got := total(aliceToken); got != 1 {
		t.Fatalf("alice sees %d nodes, want 1", got)
	}
	if got := total(bobToken); got != 0 {
		t.Errorf("bob sees %d nodes, want 0; he is being served alice's cached "+
			"figures", got)
	}
}

// ------------------------------------------------------------ traffic chart

func TestDashboardTrafficChartIsScopedToTheCaller(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, aliceSubject := seedTenant(t, env, "alice", svcID, adminToken)
	_, bobSubject := seedTenant(t, env, "bob", svcID, adminToken)

	recordUsage(t, env, aliceSubject, 1<<30, 0)
	recordUsage(t, env, bobSubject, 99<<30, 0)

	res := env.get(t, "/api/v1/dashboard/traffic-chart?period=24h", aliceToken)
	if res.Code != http.StatusOK {
		t.Fatalf("traffic-chart = %d: %s", res.Code, res.Body)
	}
	var out struct {
		DataPoints []struct {
			UplinkBytes   int64 `json:"uplink_bytes"`
			DownlinkBytes int64 `json:"downlink_bytes"`
		} `json:"data_points"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.DataPoints) == 0 {
		t.Fatal("alice's chart has no points at all, so it cannot show whether " +
			"bob's traffic was excluded or everything was")
	}
	var sum int64
	for _, p := range out.DataPoints {
		sum += p.UplinkBytes + p.DownlinkBytes
	}
	if sum != 1<<30 {
		t.Errorf("alice's chart totals %d bytes, want %d; bob's 99 GiB is being "+
			"plotted on her dashboard", sum, int64(1)<<30)
	}
}

// ---------------------------------------------------------------- top users
//
// The most direct disclosure of the four: this route returns other tenants'
// subjects BY NAME, not just as a count.

func TestDashboardTopUsersDoesNotNameOtherTenantsSubjects(t *testing.T) {
	env, adminToken, svcID := newSubjectEnv(t)
	aliceToken, aliceSubject := seedTenant(t, env, "alice", svcID, adminToken)
	_, bobSubject := seedTenant(t, env, "bob", svcID, adminToken)

	recordUsage(t, env, aliceSubject, 1<<30, 0)
	recordUsage(t, env, bobSubject, 99<<30, 0)

	res := env.get(t, "/api/v1/dashboard/top-users", aliceToken)
	if res.Code != http.StatusOK {
		t.Fatalf("top-users = %d: %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "bob-customer") {
		t.Errorf("alice's top-users names bob's customer: %s", res.Body)
	}

	var out struct {
		TopUsers []struct {
			ID   int64  `json:"subject_id"`
			Name string `json:"name"`
		} `json:"top_users"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, u := range out.TopUsers {
		if u.ID == bobSubject {
			t.Errorf("alice's top-users includes bob's subject %d (%q)", u.ID, u.Name)
		}
	}
	if len(out.TopUsers) != 1 || out.TopUsers[0].ID != aliceSubject {
		t.Errorf("alice's top-users = %+v, want exactly her own subject", out.TopUsers)
	}
}
