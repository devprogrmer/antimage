package wireguard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// The service key uses a seed no subject uses, so the interface own private
// key -- which legitimately appears in the config -- cannot be mistaken for a
// subscriber key leaking into it.
const testParams = `{"port":51820,"subnet":"10.8.0.1/24","private_key":"WGBnbnV8g4qRmJ+mrbS7wsnQ197l7PP6Bg0UGyIpMHc="}`

func desiredWith(t *testing.T, users int) adapter.Desired {
	t.Helper()
	d := adapter.Desired{
		SchemaVersion: 1, Revision: 1, NodeID: 1,
		Services: []adapter.Service{
			{ID: 10, Kind: "wireguard", Enabled: true, Params: json.RawMessage(testParams)},
		},
	}
	for i := 1; i <= users; i++ {
		d.Subjects = append(d.Subjects, adapter.Subject{
			ID: int64(i),
			Credentials: []adapter.Credential{{
				Kind: "keypair",
				// A REAL 32-byte key. The previous fixture was a 43-character
				// string that base64 cannot decode at all; it passed only
				// because the adapter copied the credential straight into the
				// peer's PublicKey field instead of deriving from it. Once the
				// derivation was fixed the fixture stopped being usable, which
				// is the fixture telling the truth for the first time.
				Value: testPrivateKey(i),
			}},
		})
	}
	return d
}

// testPrivateKey returns a deterministic, valid curve25519 private key.
//
// Deterministic so a failure reproduces; valid so the adapter's derivation
// succeeds and the test exercises the real path rather than the error branch.
func testPrivateKey(seed int) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte((i*7 + seed*31 + 1) % 251)
	}
	// Clamp as curve25519 expects, so wg would accept it too.
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64
	return base64.StdEncoding.EncodeToString(raw)
}

// WireGuard must actually converge after the initial install.
//
// needsUpdate previously returned "no change needed" for every managed
// service, so a peer added to the desired document was never added to the
// interface and a revoked peer was never removed. The adapter installed once
// and then ignored the world.
func TestPlanConvergesAfterInstall(t *testing.T) {
	a := New(nil, t.TempDir(), t.TempDir())
	obs := adapter.Observed{Services: []adapter.ObservedService{
		{ID: 10, Present: true, Managed: true, Checksum: "stale-checksum"},
	}}

	plan, err := a.Plan(context.Background(), desiredWith(t, 2), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("no steps planned for a membership change; peers would never " +
			"be added and a revoked peer would never be removed")
	}
}

// A config already on disk that the interface never came up with is not
// convergence: the file says what should be running, not what is.
func TestPlanRestartsWhenTheInterfaceNeverCameUp(t *testing.T) {
	dir := t.TempDir()
	a := New(nil, t.TempDir(), dir)
	d := desiredWith(t, 1)

	var params ServiceParams
	if err := json.Unmarshal(d.Services[0].Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	rendered, err := GenerateConfig(10, params, a.buildPeerList(d.Services[0], d.Subjects))
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	// The file matches desired, but nothing was ever applied.
	obs := adapter.Observed{Services: []adapter.ObservedService{
		{ID: 10, Present: true, Managed: true, Checksum: renderedChecksum(rendered)},
	}}

	plan, err := a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("reported converged while the interface never loaded the config")
	}
	if plan.MaxDisruption() < adapter.DisruptRestart {
		t.Errorf("planned %v, want restart", plan.MaxDisruption())
	}
}

// Revoking a peer must be restart-class: wg keeps serving a peer until it is
// explicitly told otherwise, so removing one from the file alone leaves the
// revoked user connected.
func TestRemovingAPeerIsRestartClass(t *testing.T) {
	dir := t.TempDir()
	a := New(nil, t.TempDir(), dir)

	two := desiredWith(t, 2)
	var params ServiceParams
	if err := json.Unmarshal(two.Services[0].Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	peers := a.buildPeerList(two.Services[0], two.Subjects)
	rendered, err := GenerateConfig(10, params, peers)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	// The interface is up and serving both peers.
	if err := a.recordApplied(10, renderedChecksum(rendered), mustShape(t, params), extractPublicKeys(peers)); err != nil {
		t.Fatalf("recordApplied: %v", err)
	}
	obs := adapter.Observed{Services: []adapter.ObservedService{
		{ID: 10, Present: true, Managed: true, Checksum: renderedChecksum(rendered)},
	}}

	// Now desired drops to one peer.
	plan, err := a.Plan(context.Background(), desiredWith(t, 1), obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatal("revoking a peer planned nothing")
	}
	if plan.MaxDisruption() < adapter.DisruptRestart {
		t.Errorf("revocation planned as %v, want restart; the revoked peer would "+
			"stay connected", plan.MaxDisruption())
	}
}

// A converged interface must plan nothing, or the node reconciles forever.
func TestConvergedStatePlansNothing(t *testing.T) {
	dir := t.TempDir()
	a := New(nil, t.TempDir(), dir)
	d := desiredWith(t, 2)

	var params ServiceParams
	if err := json.Unmarshal(d.Services[0].Params, &params); err != nil {
		t.Fatalf("params: %v", err)
	}
	peers := a.buildPeerList(d.Services[0], d.Subjects)
	rendered, err := GenerateConfig(10, params, peers)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	sum := renderedChecksum(rendered)
	if err := a.recordApplied(10, sum, mustShape(t, params), extractPublicKeys(peers)); err != nil {
		t.Fatalf("recordApplied: %v", err)
	}
	obs := adapter.Observed{Services: []adapter.ObservedService{
		{ID: 10, Present: true, Managed: true, Checksum: sum},
	}}

	plan, err := a.Plan(context.Background(), d, obs)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.IsEmpty() {
		t.Errorf("converged state still plans %+v", plan.Steps)
	}
}

// mustShape renders this service's shape -- its checksum with no peers.
//
// Tests record it alongside the applied checksum because that is what a real
// Apply records, and because without it every peer addition is classified as a
// structural change and restarts instead of hot-syncing.
func mustShape(t *testing.T, params ServiceParams) string {
	t.Helper()
	shape, err := shapeChecksum(10, params)
	if err != nil {
		t.Fatalf("shapeChecksum: %v", err)
	}
	return shape
}
