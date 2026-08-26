package nodes

import (
	"errors"
	"fmt"
	"math/bits"
)

// Billable computation (C3, implementing AD-2).
//
//	billable = raw * node_coef * service_coef * subject_coef * reseller_coef * outbound_coef
//
// Never stored. The same discipline as the reseller balance, which is
// SUM(delta) and never a cached column: a stored billable figure drifts the
// moment a coefficient changes and silently rewrites history. Everything here
// derives from the raw bytes and the coefficients in force at read time.

// CoefficientUnit is x1.0 in basis points.
//
// Basis points rather than floats for the reason the credit ledger refuses
// floats for money: billable traffic is money. x0.0001 resolution in exact
// integers, with no representation error accumulating across a billing period.
const CoefficientUnit = 10000

// ErrCoefficientOverflow means the bill does not fit in an int64.
//
// Returned rather than saturating or wrapping. A silently wrong number here is
// an invoice, and there is no value that is a safe guess.
var ErrCoefficientOverflow = errors.New("billable computation overflowed int64")

// Factors are the five coefficients applied to one quantity of raw bytes, in
// the order AD-2 names them plus outbound (Phase F §27).
//
// A struct rather than five int64 arguments because they are mutually
// transposable at the call site and nothing would catch it: swapping node and
// subject compiles, runs, and produces a bill that is wrong only when the two
// happen to differ.
type Factors struct {
	Node     int64
	Service  int64
	Subject  int64
	Reseller int64
	Outbound int64
}

// UnitFactors is every coefficient at x1.0, which computes exactly what the
// system computed before AD-2 and Phase F.
func UnitFactors() Factors {
	return Factors{
		Node:     CoefficientUnit,
		Service:  CoefficientUnit,
		Subject:  CoefficientUnit,
		Reseller: CoefficientUnit,
		Outbound: CoefficientUnit,
	}
}

// All returns the factors in application order, for callers that render the
// derivation. Section 11 requires the calculation be shown, not hidden.
func (f Factors) All() []int64 {
	return []int64{f.Node, f.Service, f.Subject, f.Reseller, f.Outbound}
}

// Billable applies the five coefficients to raw bytes.
//
// Progressive rather than one big product, as the plan requires: raw with all
// five coefficients multiplied first overflows int64 long before the RESULT
// would, so a bill that is perfectly representable would fail to compute.
// Each step is exact in 128 bits and divides before the next multiply.
//
// The cost is that each step truncates below one byte, so a result can be up to
// five bytes under the infinitely-precise value. That is deliberate and it
// rounds DOWN: an operator overcharging a customer by five bytes because of
// rounding is a worse failure than undercharging by it.
func Billable(raw int64, f Factors) (int64, error) {
	if raw < 0 {
		// Usage is a count of bytes. A negative one is a corrupted caller, not
		// a refund, and multiplying it out would launder the corruption into a
		// plausible-looking credit.
		return 0, fmt.Errorf("negative raw bytes %d", raw)
	}
	v := raw
	for i, k := range f.All() {
		if k < 0 {
			return 0, fmt.Errorf("negative coefficient %d at position %d", k, i)
		}
		var err error
		if v, err = mulDiv(v, k, CoefficientUnit); err != nil {
			return 0, err
		}
	}
	return v, nil
}

// mulDiv computes x*num/den exactly, without overflowing on the intermediate.
//
// bits.Mul64 gives the full 128-bit product, so the multiply cannot lose
// anything; bits.Div64 then brings it back down. Div64 PANICS when the quotient
// would not fit in 64 bits, so the hi >= den check below is not defensive
// clutter -- it is what turns an unrepresentable bill into an error instead of
// a crash in the accounting sweeper.
func mulDiv(x, num, den int64) (int64, error) {
	if den == 0 {
		return 0, errors.New("zero denominator")
	}
	hi, lo := bits.Mul64(uint64(x), uint64(num))
	if hi >= uint64(den) {
		return 0, fmt.Errorf("%w: %d * %d / %d", ErrCoefficientOverflow, x, num, den)
	}
	q, _ := bits.Div64(hi, lo, uint64(den))
	if q > uint64(maxInt64) {
		return 0, fmt.Errorf("%w: %d * %d / %d", ErrCoefficientOverflow, x, num, den)
	}
	return int64(q), nil
}

const maxInt64 = int64(^uint64(0) >> 1)

// Threshold converts a quota expressed in BILLABLE bytes into the raw-byte
// figure that reaches it, for one combination of coefficients.
//
// This is C4's arithmetic, and it exists because quota_used_bytes is a stored
// counter of RAW bytes. Comparing it against a billable quota directly would
// compare two different quantities and coefficients would have no effect on
// enforcement -- the exact class of bug that made max_quota_bytes decorative in
// the reseller engine, where a number was recorded somewhere other than the
// state the decision reads from.
//
// Note what it does NOT do. A subject whose traffic spans several nodes or
// services has no single set of coefficients, so there is no single threshold
// for them and this function must not be used to build one; see
// BillableForSubject, which sums per group. It is correct only where one
// combination governs all of the bytes being compared.
//
// Rounds UP, so a subject is never frozen a byte early.
func Threshold(billableQuota int64, f Factors) (int64, error) {
	if billableQuota < 0 {
		return 0, fmt.Errorf("negative quota %d", billableQuota)
	}
	v := billableQuota
	for i, k := range f.All() {
		if k < 0 {
			return 0, fmt.Errorf("negative coefficient %d at position %d", k, i)
		}
		if k == 0 {
			// Traffic here is free, so no amount of it ever reaches the quota.
			// Saying "infinity" honestly beats dividing by zero.
			return maxInt64, nil
		}
		var err error
		if v, err = mulDivCeil(v, CoefficientUnit, k); err != nil {
			return 0, err
		}
	}
	return v, nil
}

// mulDivCeil is mulDiv rounding away from zero.
func mulDivCeil(x, num, den int64) (int64, error) {
	if den == 0 {
		return 0, errors.New("zero denominator")
	}
	hi, lo := bits.Mul64(uint64(x), uint64(num))
	if hi >= uint64(den) {
		return 0, fmt.Errorf("%w: %d * %d / %d", ErrCoefficientOverflow, x, num, den)
	}
	q, r := bits.Div64(hi, lo, uint64(den))
	if r != 0 {
		q++
	}
	if q > uint64(maxInt64) {
		return 0, fmt.Errorf("%w: %d * %d / %d", ErrCoefficientOverflow, x, num, den)
	}
	return int64(q), nil
}
