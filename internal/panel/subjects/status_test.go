package subjects

import (
	"testing"
	"time"
)

// Status is derived, never stored, so these are pure table cases over the
// columns that actually govern service. The precedence is the contract: it is
// what the list badge, the detail card and any future automation all read, and
// a change here silently changes what an operator is told.

func at(unix int64) *time.Time {
	t := time.Unix(unix, 0).UTC()
	return &t
}

func secs(n int64) *int64 { return &n }

func TestStatusPrecedence(t *testing.T) {
	now := time.Unix(1000, 0).UTC()

	for _, tc := range []struct {
		name string
		subj Subject
		want Status
	}{
		{
			name: "plain enabled subject",
			subj: Subject{Enabled: true},
			want: StatusActive,
		},
		{
			name: "not yet started",
			subj: Subject{Enabled: true, OnHoldSeconds: secs(2592000)},
			want: StatusOnHold,
		},
		{
			name: "past its expiry",
			subj: Subject{Enabled: true, ExpiresAt: at(999)},
			want: StatusExpired,
		},
		{
			name: "switched off",
			subj: Subject{Enabled: false},
			want: StatusDisabled,
		},
		{
			name: "revoked",
			subj: Subject{Enabled: true, FrozenAt: at(900)},
			want: StatusFrozen,
		},
		// Frozen wins over disabled: it is the state the operator did not
		// necessarily choose, and the only one carrying a reason.
		{
			name: "frozen and disabled reports frozen",
			subj: Subject{Enabled: false, FrozenAt: at(900)},
			want: StatusFrozen,
		},
		{
			name: "frozen and expired reports frozen",
			subj: Subject{Enabled: true, FrozenAt: at(900), ExpiresAt: at(999)},
			want: StatusFrozen,
		},
		{
			name: "disabled and expired reports disabled",
			subj: Subject{Enabled: false, ExpiresAt: at(999)},
			want: StatusDisabled,
		},
		// A disabled subject that was sold on hold has still not started, but
		// "disabled" is the more useful answer: it is why they cannot connect.
		{
			name: "disabled and on hold reports disabled",
			subj: Subject{Enabled: false, OnHoldSeconds: secs(60)},
			want: StatusDisabled,
		},
		{
			name: "expiry exactly now counts as expired",
			subj: Subject{Enabled: true, ExpiresAt: at(1000)},
			want: StatusExpired,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.subj.Status(now); got != tc.want {
				t.Errorf("Status() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The whole mechanism depends on this: an on-hold subject must reach a node,
// because connecting is what starts their clock. If Active excluded them they
// would never be served, never report usage, and never activate -- a plan that
// can never begin.
func TestAnOnHoldSubjectIsEntitledToService(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	s := Subject{Enabled: true, OnHoldSeconds: secs(2592000)}

	if !s.Active(now) {
		t.Error("an on-hold subject is not entitled to service, so it can never " +
			"connect, never report usage, and never start its own plan")
	}
	if s.Status(now) != StatusOnHold {
		t.Errorf("Status() = %q, want %q", s.Status(now), StatusOnHold)
	}
}

// And the states that genuinely deny service still do.
func TestStatusAgreesWithEntitlement(t *testing.T) {
	now := time.Unix(1000, 0).UTC()

	for _, tc := range []struct {
		name       string
		subj       Subject
		wantActive bool
	}{
		{"active", Subject{Enabled: true}, true},
		{"on hold", Subject{Enabled: true, OnHoldSeconds: secs(60)}, true},
		{"frozen", Subject{Enabled: true, FrozenAt: at(900)}, false},
		{"disabled", Subject{Enabled: false}, false},
		{"expired", Subject{Enabled: true, ExpiresAt: at(999)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.subj.Active(now); got != tc.wantActive {
				t.Errorf("Active() = %v, want %v for status %q",
					got, tc.wantActive, tc.subj.Status(now))
			}
		})
	}
}
