package nodes

import (
	"errors"
	"math"
	"testing"
)

// C3's arithmetic. An error here is an invoice, so these are about exactness
// and about what happens at the edges rather than about the happy path.

func TestBillableAtUnityChangesNothing(t *testing.T) {
	// The property that made 00027 safe to ship ahead of this reader: every
	// coefficient at x1.0 must compute exactly what the system computed before
	// AD-2 existed.
	for _, raw := range []int64{0, 1, 999, 1 << 40, math.MaxInt64} {
		got, err := Billable(raw, UnitFactors())
		if err != nil {
			t.Fatalf("Billable(%d, unity): %v", raw, err)
		}
		if got != raw {
			t.Errorf("Billable(%d, unity) = %d, want %d; unity is not the "+
				"identity and every pre-AD-2 bill just changed", raw, got, raw)
		}
	}
}

func TestBillableAppliesEveryFactor(t *testing.T) {
	tests := []struct {
		name string
		raw  int64
		f    Factors
		want int64
	}{
		{"node x2", 1000, Factors{20000, 10000, 10000, 10000}, 2000},
		{"service x2", 1000, Factors{10000, 20000, 10000, 10000}, 2000},
		{"subject x2", 1000, Factors{10000, 10000, 20000, 10000}, 2000},
		{"reseller x2", 1000, Factors{10000, 10000, 10000, 20000}, 2000},
		{"all x2 compounds", 1000, Factors{20000, 20000, 20000, 20000}, 16000},
		{"half", 1000, Factors{5000, 10000, 10000, 10000}, 500},
		{"x1.5 x0.5 cancels", 1000, Factors{15000, 5000, 10000, 10000}, 750},
		// x0.0001 is the finest the basis-point scale expresses.
		{"minimum resolution", 10000, Factors{1, 10000, 10000, 10000}, 1},
		{"free", 1000, Factors{0, 10000, 10000, 10000}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Billable(tt.raw, tt.f)
			if err != nil {
				t.Fatalf("Billable: %v", err)
			}
			if got != tt.want {
				t.Errorf("Billable(%d, %+v) = %d, want %d", tt.raw, tt.f, got, tt.want)
			}
		})
	}
}

// Each factor must occupy its own position. Transposing two of them compiles
// and runs, and produces a wrong bill only when they differ -- which is exactly
// when it matters.
func TestBillableFactorsAreNotInterchangeable(t *testing.T) {
	base := Factors{Node: 20000, Service: 10000, Subject: 10000, Reseller: 10000}
	got, err := Billable(1000, base)
	if err != nil {
		t.Fatalf("Billable: %v", err)
	}
	// The product is commutative, so position cannot be caught by arithmetic
	// alone -- it is caught by what the API reports back.
	if all := base.All(); all[0] != base.Node || all[1] != base.Service ||
		all[2] != base.Subject || all[3] != base.Reseller {
		t.Errorf("All() = %v, want node, service, subject, reseller in that order; "+
			"the UI renders this as the derivation and would mislabel every factor", all)
	}
	if got != 2000 {
		t.Errorf("Billable = %d, want 2000", got)
	}
}

// The plan asks for a test at the int64 boundary, because "an overflow here is
// a bill, not a rendering artifact".
func TestBillableOverflow(t *testing.T) {
	// A result that does not fit must be an error, not a wrapped negative.
	_, err := Billable(math.MaxInt64, Factors{20000, 10000, 10000, 10000})
	if !errors.Is(err, ErrCoefficientOverflow) {
		t.Errorf("doubling MaxInt64 gave err = %v, want ErrCoefficientOverflow", err)
	}

	// THE case the plan warns about: naively multiplying all five terms first
	// overflows long before the RESULT would. raw x 1.0 x 1.0 x 1.0 x 1.0 with
	// a big raw is perfectly representable and must compute.
	//
	// 10^18 x 10000^4 is 10^34, far past int64, so anything that forms the full
	// product before dividing fails this.
	const big = int64(1_000_000_000_000_000_000)
	got, err := Billable(big, UnitFactors())
	if err != nil {
		t.Fatalf("a representable bill failed to compute: %v; the implementation "+
			"is forming the whole product before dividing", err)
	}
	if got != big {
		t.Errorf("Billable(%d, unity) = %d, want %d", big, got, big)
	}

	// And one that genuinely does not fit is refused rather than truncated.
	if _, err := Billable(big, Factors{100000, 100000, 100000, 100000}); err == nil {
		t.Error("a bill of 10^22 bytes was reported as representable")
	}
}

// Rounding direction is a policy, not an accident: down, so the operator never
// overcharges because of truncation.
func TestBillableRoundsDown(t *testing.T) {
	// 1 byte at x0.5 is half a byte.
	got, err := Billable(1, Factors{5000, 10000, 10000, 10000})
	if err != nil {
		t.Fatalf("Billable: %v", err)
	}
	if got != 0 {
		t.Errorf("Billable(1, x0.5) = %d, want 0; rounding up overcharges", got)
	}
}

func TestBillableRefusesNegatives(t *testing.T) {
	if _, err := Billable(-1, UnitFactors()); err == nil {
		t.Error("negative raw bytes were accepted; a corrupted counter would be " +
			"laundered into a credit")
	}
	if _, err := Billable(100, Factors{-1, 10000, 10000, 10000}); err == nil {
		t.Error("a negative coefficient was accepted")
	}
}

// ---------------------------------------------------------------- threshold

// Threshold is the inverse: how much RAW traffic reaches a billable quota.
func TestThresholdInvertsBillable(t *testing.T) {
	for _, f := range []Factors{
		UnitFactors(),
		{20000, 10000, 10000, 10000},
		{10000, 5000, 10000, 10000},
		{15000, 15000, 10000, 10000},
	} {
		const quota = 1_000_000
		raw, err := Threshold(quota, f)
		if err != nil {
			t.Fatalf("Threshold(%d, %+v): %v", quota, f, err)
		}
		// At the threshold the bill has reached the quota.
		billed, err := Billable(raw, f)
		if err != nil {
			t.Fatalf("Billable: %v", err)
		}
		if billed < quota {
			t.Errorf("at the threshold %d, billable is %d which is under the "+
				"quota %d; the subject would never be frozen", raw, billed, quota)
		}
	}
}

// Rounds up, so nobody is frozen a byte early.
func TestThresholdRoundsUp(t *testing.T) {
	// Quota 1 at x2.0: half a raw byte reaches it, so the threshold is 1.
	raw, err := Threshold(1, Factors{20000, 10000, 10000, 10000})
	if err != nil {
		t.Fatalf("Threshold: %v", err)
	}
	if raw != 1 {
		t.Errorf("Threshold(1, x2.0) = %d, want 1; rounding down would freeze a "+
			"subject before they used anything", raw)
	}
}

// A zero coefficient means the traffic is free, so no amount of it reaches the
// quota. Dividing by zero is the wrong way to say that.
func TestThresholdWithAFreeCoefficient(t *testing.T) {
	raw, err := Threshold(1000, Factors{0, 10000, 10000, 10000})
	if err != nil {
		t.Fatalf("Threshold: %v", err)
	}
	if raw != math.MaxInt64 {
		t.Errorf("Threshold with a x0.0 coefficient = %d, want MaxInt64; free "+
			"traffic must never reach a quota", raw)
	}
}
