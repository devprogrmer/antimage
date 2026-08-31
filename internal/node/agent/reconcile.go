package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Run records one convergence attempt. It is reported to the panel, which
// uses Converged to decide whether applied_revision may advance (invariant 7).
type Run struct {
	TargetRevision int64
	Steps          []adapter.StepResult
	Converged      bool
	Deferred       bool
	Err            string
}

type ReconcileOptions struct {
	// MaxRetries bounds retries of a single failing step before the run is
	// abandoned and the node reported Degraded.
	MaxRetries int
	// RetryBase is the first backoff interval; it doubles per attempt.
	RetryBase time.Duration
	// AllowDisruptive reports whether restart-class steps may run now. Nil
	// means always. This implements the maintenance window from spec 4.1.
	AllowDisruptive func(time.Time) bool
}

type Reconciler struct {
	ads  *Registry
	clk  Clock
	opts ReconcileOptions
}

func NewReconciler(ads *Registry, clk Clock, opts ReconcileOptions) *Reconciler {
	if opts.MaxRetries < 1 {
		opts.MaxRetries = 3
	}
	if opts.RetryBase <= 0 {
		opts.RetryBase = time.Second
	}
	return &Reconciler{ads: ads, clk: clk, opts: opts}
}

func (r *Reconciler) disruptiveAllowed() bool {
	if r.opts.AllowDisruptive == nil {
		return true
	}
	return r.opts.AllowDisruptive(r.clk.Now())
}

// Converge runs a complete, isolated Observe -> Plan -> Apply -> confirm cycle
// for EACH registered adapter, and merges the outcomes into one Run.
//
// Per adapter, not across them, and that is the whole design. adapter.Observed
// carries no Kind, so an adapter shown another's observations cannot tell them
// from its own -- and every adapter's removal pass plans a removal for observed
// services it does not recognise. Merging observations would have Xray plan
// remove_service for a WireGuard interface and mean it. Here an adapter is
// never shown another's observations at all, so isolation holds structurally
// rather than by each adapter remembering to filter.
//
// The run is the node's, not any one adapter's: the panel advances
// applied_revision only when the whole node has converged, so Converged is true
// only when EVERY adapter's confirmation plan is empty.
func (r *Reconciler) Converge(ctx context.Context, desired adapter.Desired) (Run, error) {
	run := Run{TargetRevision: desired.Revision, Converged: true}

	// Two categories of failure, kept apart because callers treat them
	// differently and always have.
	//
	// hardErr is a failure to carry out the work: Observe, Plan or Apply broke.
	// It is returned as a Go error.
	//
	// A confirmation plan that is not empty is NOT that. The work was done and
	// the node is simply not there yet, which is reported through Run.Converged
	// and Run.Err and no error at all -- because applied_revision is gated on
	// Converged, and turning "not finished" into a returned error would make an
	// in-progress node indistinguishable from a broken one.
	hardErr := ""

	// Step sequence numbers are assigned here, across every adapter, because
	// they are the panel's only ordering for node_apply_steps and no adapter
	// can number steps it does not know about. Most adapters never set Seq at
	// all, so before this every step in a run reported as step 0.
	seq := 0

	for _, ad := range r.ads.Adapters() {
		kind := string(ad.Descriptor().Kind)

		observed, err := ad.Observe(ctx)
		if err != nil {
			run.Converged = false
			msg := fmt.Sprintf("%s: observe: %v", kind, err)
			run.Err = appendErr(run.Err, msg)
			hardErr = appendErr(hardErr, msg)
			// One adapter failing to observe says nothing about the others, and
			// a node serving four protocols should not stop reconciling three
			// of them because the fourth's binary is missing.
			continue
		}

		plan, err := ad.Plan(ctx, desired, observed)
		if err != nil {
			run.Converged = false
			msg := fmt.Sprintf("%s: plan: %v", kind, err)
			run.Err = appendErr(run.Err, msg)
			hardErr = appendErr(hardErr, msg)
			continue
		}

		if plan.IsEmpty() {
			continue // this adapter has nothing to do
		}

		// Deferral is per adapter for the same reason it is per plan: a plan of
		// only DisruptNone steps applies immediately regardless of the window,
		// so a user disable on one protocol never waits for 04:00 because a
		// different protocol wants to move a port.
		if plan.MaxDisruption() >= adapter.DisruptRestart && !r.disruptiveAllowed() {
			run.Deferred = true
			run.Converged = false
			continue
		}

		failed := false
		for _, step := range plan.Steps {
			step.Seq = seq
			seq++
			result, err := r.applyWithRetry(ctx, ad, step)
			run.Steps = append(run.Steps, result)
			if err != nil {
				failed = true
				run.Err = appendErr(run.Err, result.Err)
				hardErr = appendErr(hardErr, result.Err)
				// One failure must not block unrelated steps, so continue
				// rather than abort; the run simply will not converge.
			}
		}
		if failed {
			run.Converged = false
			// No confirmation pass: it would re-report the same outstanding
			// work as a second, confusing error.
			continue
		}

		// Confirmation: re-observe and re-plan rather than trust that Apply
		// succeeded. This is what makes partial or drifted application visible
		// instead of silently accepted, and is what applied_revision is gated
		// on.
		observed, err = ad.Observe(ctx)
		if err != nil {
			run.Converged = false
			msg := fmt.Sprintf("%s: post-apply observe: %v", kind, err)
			run.Err = appendErr(run.Err, msg)
			hardErr = appendErr(hardErr, msg)
			continue
		}
		confirm, err := ad.Plan(ctx, desired, observed)
		if err != nil {
			run.Converged = false
			msg := fmt.Sprintf("%s: post-apply plan: %v", kind, err)
			run.Err = appendErr(run.Err, msg)
			hardErr = appendErr(hardErr, msg)
			continue
		}
		if !confirm.IsEmpty() {
			run.Converged = false
			run.Err = appendErr(run.Err, fmt.Sprintf(
				"%s: %d steps still outstanding after apply", kind, len(confirm.Steps)))
		}
	}

	if hardErr != "" {
		return run, errors.New(hardErr)
	}
	// A run that merely has not converged yet -- deferred for a window, or with
	// a confirmation plan still holding work -- is not an error. Reporting it as
	// one would put every node into Degraded outside maintenance hours.
	return run, nil
}

// appendErr joins step errors so one adapter's failure does not erase another's.
//
// The previous single-adapter reconciler assigned run.Err, so with several
// adapters the last failure would have hidden the rest -- and the one an
// operator needs is rarely the last.
func appendErr(existing, add string) string {
	if add == "" {
		return existing
	}
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

func (r *Reconciler) applyWithRetry(
	ctx context.Context, ad adapter.Adapter, step adapter.Step,
) (adapter.StepResult, error) {
	var (
		last    adapter.StepResult
		lastErr error
		backoff = r.opts.RetryBase
	)
	for attempt := 1; attempt <= r.opts.MaxRetries; attempt++ {
		started := r.clk.Now()
		result, err := ad.Apply(ctx, step)
		// Identity, cost, and timing of a step belong to the step and to the
		// reconciler that ran it, never to the adapter's own bookkeeping. An
		// adapter that forgets to fill these in (the stub does) would
		// otherwise report every step to the panel as an unnamed step of
		// unknown disruption, which is exactly the data node_apply_steps
		// exists to hold.
		result.Seq = step.Seq
		result.Kind = step.Kind
		result.Disruption = step.Disruption
		result.Duration = r.clk.Now().Sub(started)

		if err == nil && result.OK {
			return result, nil
		}
		last, lastErr = result, err
		if lastErr == nil {
			lastErr = errors.New(result.Err)
		}

		if attempt == r.opts.MaxRetries {
			break
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-r.clk.After(backoff):
		}
		backoff *= 2
	}
	return last, fmt.Errorf("step %d (%s) failed after %d attempts: %w",
		step.Seq, step.Kind, r.opts.MaxRetries, lastErr)
}
