package auth

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// Defaults from the spec's global constraints. A reseller panel is a
// credential-stuffing target, so both the account and the source IP are
// limited: the account limit stops one password being guessed, the IP limit
// stops one attacker spraying many accounts.
const (
	Window              = 15 * time.Minute
	AccountFailureLimit = 5
	IPFailureLimit      = 20
	BaseBackoff         = time.Second
	MaxBackoff          = 300 * time.Second
)

type Limiter struct {
	store *store.Store
	now   func() time.Time
}

func NewLimiter(s *store.Store, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{store: s, now: now}
}

func (l *Limiter) countSince(ctx context.Context, kind, subject string, since time.Time) (int, error) {
	var n int
	err := l.store.Read().QueryRowContext(ctx,
		`SELECT count(*) FROM login_attempts
		  WHERE kind = ? AND subject = ? AND failed_at >= ?`,
		kind, strings.ToLower(subject), since.Unix()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count %s attempts: %w", kind, err)
	}
	return n, nil
}

// backoff doubles per failure past the limit, capped at MaxBackoff.
func backoff(failures, limit int) time.Duration {
	if failures < limit {
		return 0
	}
	d := BaseBackoff
	for i := limit; i < failures; i++ {
		d *= 2
		if d >= MaxBackoff {
			return MaxBackoff
		}
	}
	return d
}

// Check returns how long the caller must wait. Zero means allowed.
func (l *Limiter) Check(ctx context.Context, username, ip string) (time.Duration, error) {
	since := l.now().UTC().Add(-Window)

	accountFailures, err := l.countSince(ctx, "account", username, since)
	if err != nil {
		return 0, err
	}
	ipFailures, err := l.countSince(ctx, "ip", ip, since)
	if err != nil {
		return 0, err
	}

	wait := backoff(accountFailures, AccountFailureLimit)
	if d := backoff(ipFailures, IPFailureLimit); d > wait {
		wait = d
	}
	return wait, nil
}

func (l *Limiter) RecordFailure(ctx context.Context, username, ip string) error {
	at := l.now().UTC().Unix()
	return l.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO login_attempts (kind, subject, failed_at) VALUES ('account', ?, ?)`,
			strings.ToLower(username), at); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO login_attempts (kind, subject, failed_at) VALUES ('ip', ?, ?)`,
			strings.ToLower(ip), at)
		return err
	})
}

// Reset clears the account's failure counter after a successful login, and
// opportunistically prunes rows that have aged out of the window.
//
// It deliberately does NOT clear kind='ip' rows. A successful authentication
// proves that one account's credentials were correct; it says nothing about
// whether the source IP is benign. login_attempts has no per-(ip, username)
// pairing, so an IP-wide delete here would wipe every other username's
// contribution to that IP's failure count too. That is exactly the attack
// the IP limit exists to stop: spray 20 usernames from one IP to trip the
// limit, then use a correct guess among them (or any other successful login
// from that IP, including an unrelated user behind the same NAT) to clear
// the bucket and resume the spray. Do not add an IP delete back here.
//
// The accepted cost: a legitimate user behind a shared NAT that has
// genuinely accumulated IPFailureLimit failures in the window will still
// have to wait out the window after authenticating successfully. IP
// failures decay by time alone. That is the correct trade for a
// deliberately coarse, shared-fate control.
func (l *Limiter) Reset(ctx context.Context, username, ip string) error {
	cutoff := l.now().UTC().Add(-Window).Unix()
	return l.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM login_attempts WHERE kind = 'account' AND subject = ?`,
			strings.ToLower(username)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM login_attempts WHERE failed_at < ?`, cutoff)
		return err
	})
}
