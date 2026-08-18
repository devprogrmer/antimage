package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func newSessions(t *testing.T) (*Sessions, *store.Store, int64, *fakeClock) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	adminID := insertTestAdmin(t, s)
	clk := &fakeClock{now: time.Unix(1_700_000_000, 0).UTC()}
	return NewSessions(s, clk.Now), s, adminID, clk
}

func TestCreateReturnsOpaqueTokenNotStoredPlain(t *testing.T) {
	sess, s, adminID, _ := newSessions(t)
	ctx := context.Background()
	token, err := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(token) < 32 {
		t.Errorf("token length %d is too short to be 32 random bytes", len(token))
	}
	var n int
	if err := s.Read().QueryRow(
		`SELECT count(*) FROM sessions WHERE token_hash = ?`, []byte(token),
	).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Fatal("the raw token was stored — only its SHA-256 may be persisted")
	}
}

func TestLookupAcceptsValidTokenAndRejectsGarbage(t *testing.T) {
	sess, _, adminID, _ := newSessions(t)
	ctx := context.Background()
	token, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")

	got, err := sess.Lookup(ctx, token)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.AdminID != adminID {
		t.Errorf("AdminID = %d, want %d", got.AdminID, adminID)
	}
	if _, err := sess.Lookup(ctx, "not-a-real-token"); err == nil {
		t.Error("Lookup accepted a garbage token")
	}
}

func TestRevokeTakesEffectImmediately(t *testing.T) {
	sess, _, adminID, _ := newSessions(t)
	ctx := context.Background()
	token, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")
	s, err := sess.Lookup(ctx, token)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if err := sess.Revoke(ctx, s.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := sess.Lookup(ctx, token); err == nil {
		t.Fatal("revoked session still validates — immediate revocation is the reason " +
			"we chose server-side sessions over JWTs")
	}
}

func TestIdleTimeoutExpiresSession(t *testing.T) {
	sess, _, adminID, clk := newSessions(t)
	ctx := context.Background()
	token, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")

	clk.advance(IdleTimeout - time.Minute)
	if _, err := sess.Lookup(ctx, token); err != nil {
		t.Fatalf("session expired early: %v", err)
	}
	// The successful lookup refreshed last_used_at, so the idle window restarts.
	clk.advance(IdleTimeout + time.Minute)
	if _, err := sess.Lookup(ctx, token); err == nil {
		t.Fatal("idle session still valid past IdleTimeout")
	}
}

func TestAbsoluteLifetimeExpiresActiveSession(t *testing.T) {
	sess, _, adminID, clk := newSessions(t)
	ctx := context.Background()
	token, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")

	// Stay active up to one hour short of the absolute lifetime.
	for elapsed := time.Hour; elapsed < AbsoluteLifetime; elapsed += time.Hour {
		clk.advance(time.Hour)
		if _, err := sess.Lookup(ctx, token); err != nil {
			t.Fatalf("session died early at %v: %v", elapsed, err)
		}
	}

	// One more hour puts the clock exactly at expires_at, where it must be dead.
	clk.advance(time.Hour)
	if _, err := sess.Lookup(ctx, token); err == nil {
		t.Fatal("session still valid at exactly expires_at; expiry must be inclusive")
	}
}

func TestRevokeAllForAdmin(t *testing.T) {
	sess, _, adminID, _ := newSessions(t)
	ctx := context.Background()
	a, _ := sess.Create(ctx, adminID, "10.0.0.1", "curl/8")
	b, _ := sess.Create(ctx, adminID, "10.0.0.2", "firefox")
	if err := sess.RevokeAllForAdmin(ctx, adminID); err != nil {
		t.Fatalf("RevokeAllForAdmin: %v", err)
	}
	for name, tok := range map[string]string{"a": a, "b": b} {
		if _, err := sess.Lookup(ctx, tok); err == nil {
			t.Errorf("session %s survived RevokeAllForAdmin", name)
		}
	}
}

// Validate must run exactly the checks Lookup does, but never touch
// last_used_at. The SSE stream re-checks its session every few seconds; if
// that refreshed the idle window, an unattended tab holding a stream open
// would never idle out and IdleTimeout would be dead for that endpoint.
func TestValidateDoesNotExtendTheIdleWindow(t *testing.T) {
	sessions, s, adminID, clk := newSessions(t)
	ctx := context.Background()
	token, err := sessions.Create(ctx, adminID, "10.0.0.1", "agent")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	readLastUsed := func() int64 {
		var v int64
		if err := s.Read().QueryRow(
			`SELECT last_used_at FROM sessions WHERE admin_id = ?`, adminID).Scan(&v); err != nil {
			t.Fatalf("read last_used_at: %v", err)
		}
		return v
	}
	original := readLastUsed()

	for i := 0; i < 5; i++ {
		clk.advance(30 * time.Minute)
		if _, err := sessions.Validate(ctx, token); err != nil {
			t.Fatalf("Validate at step %d: %v", i, err)
		}
	}
	if got := readLastUsed(); got != original {
		t.Fatalf("last_used_at moved %d -> %d: Validate extended the idle window", original, got)
	}

	// Cross IdleTimeout with no real activity. The session must be dead, which
	// is only true because the Validate calls above did not refresh it.
	clk.advance(IdleTimeout)
	if _, err := sessions.Validate(ctx, token); err == nil {
		t.Fatal("session outlived the idle window: Validate kept it alive")
	}
}

// The contrast: ordinary requests use Lookup, which must keep extending the
// window, or an actively working admin would be logged out mid-session.
func TestLookupStillExtendsTheIdleWindow(t *testing.T) {
	sessions, _, adminID, clk := newSessions(t)
	ctx := context.Background()
	token, err := sessions.Create(ctx, adminID, "10.0.0.1", "agent")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 5; i++ {
		clk.advance(IdleTimeout - time.Minute)
		if _, err := sessions.Lookup(ctx, token); err != nil {
			t.Fatalf("Lookup at step %d: %v", i, err)
		}
	}
}

// Validate must still reject a revoked session — that is its purpose on the
// stream.
func TestValidateRejectsARevokedSession(t *testing.T) {
	sessions, _, adminID, _ := newSessions(t)
	ctx := context.Background()
	token, err := sessions.Create(ctx, adminID, "10.0.0.1", "agent")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess, err := sessions.Validate(ctx, token)
	if err != nil {
		t.Fatalf("Validate before revoke: %v", err)
	}
	if err := sessions.Revoke(ctx, sess.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := sessions.Validate(ctx, token); err == nil {
		t.Fatal("a revoked session passed Validate")
	}
}
