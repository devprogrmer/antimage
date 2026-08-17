package nodes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/audit"
)

func TestTokenRedeemsExactlyOnce(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	token, err := IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req", now)
	if err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}

	gotID, err := RedeemEnrollToken(ctx, s, token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if gotID != nodeID {
		t.Errorf("node id = %d, want %d", gotID, nodeID)
	}

	if _, err := RedeemEnrollToken(ctx, s, token, now.Add(2*time.Minute)); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("second redeem err = %v, want ErrTokenInvalid — tokens are single use", err)
	}
}

func TestTokenExpiresAfterTTL(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()

	token, _ := IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req", now)
	_, err := RedeemEnrollToken(ctx, s, token, now.Add(EnrollTokenTTL+time.Second))
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired redeem err = %v, want ErrTokenInvalid", err)
	}
}

func TestUnknownTokenIsRejected(t *testing.T) {
	s, _ := newNodeFixture(t)
	if _, err := RedeemEnrollToken(context.Background(), s, "bogus", time.Now()); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestTokenIsStoredHashed(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	token, _ := IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req",
		time.Unix(1_700_000_000, 0).UTC())

	var n int
	if err := s.Read().QueryRow(
		`SELECT count(*) FROM enroll_tokens WHERE token_hash = ?`, []byte(token)).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 0 {
		t.Fatal("the raw enrollment token was stored")
	}
}

func TestIssuingTokenIsAudited(t *testing.T) {
	s, nodeID := newNodeFixture(t)
	ctx := context.Background()
	if _, err := IssueEnrollToken(ctx, s, nodeID, audit.SystemActor("test"), "req-9",
		time.Unix(1_700_000_000, 0).UTC()); err != nil {
		t.Fatalf("IssueEnrollToken: %v", err)
	}
	var action, requestID string
	if err := s.Read().QueryRow(
		`SELECT action, request_id FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&action, &requestID); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if action != "node.enroll_token" || requestID != "req-9" {
		t.Errorf("audit = %s/%s, want node.enroll_token/req-9", action, requestID)
	}
}
