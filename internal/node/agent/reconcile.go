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
	ad   adapter.Adapter
	clk  Clock
	opts ReconcileOptions
}

func NewReconciler(a adapter.Adapter, clk Clock, opts ReconcileOptions) *Reconciler {
	if opts.MaxRetries < 1 {
		opts.MaxRetries = 3
	}
	if opts.RetryBase <= 0 {
		opts.RetryBase = time.Second
	}
	return &Reconciler{ad: a, clk: clk, opts: opts}
}

func (r *Reconciler) disruptiveAllowed() bool {
	if r.opts.AllowDisruptive == nil {
		return true
	}
	return r.opts.AllowDisruptive(r.clk.Now())
}

// Converge runs Observe -> Plan -> Apply, then re-observes and re-plans to
// confirm. Converged is true only when that confirmation plan is empty, which
// is what makes partial application visible rather than silently accepted.
func (r *Reconciler) Converge(ctx context.Context, desired adapter.Desired) (Run, error) {
	run := Run{TargetRevision: desired.Revision}

	observed, err := r.ad.Observe(ctx)
	if err != nil {
		run.Err = err.Error()
		return run, fmt.Errorf("observe: %w", err)
	}

	plan, err := r.ad.Plan(ctx, desired, observed)
	if err != nil {
		run.Err = err.Error()
		return run, fmt.Errorf("plan: %w", err)
	}

	if plan.IsEmpty() {
		run.Converged = true
		return run, nil
	}

	// Defer the whole plan when its worst step needs a restart and the
	// window is closed. A plan of only DisruptNone/DisruptReload steps
	// applies immediately regardless of the window, so a user disable never
	// waits for 04:00 just because some other step in the same plan moves a
	// port.
	if plan.MaxDisruption() >= adapter.DisruptRestart && !r.disruptiveAllowed() {
		run.Deferred = true
		return run, nil
	}

	for _, step := range plan.Steps {
		result, err := r.applyWithRetry(ctx, step)
		run.Steps = append(run.Steps, result)
		if err != nil {
			run.Err = result.Err
			// One failure must not block unrelated steps, so continue rather
			// than abort; the run simply will not converge.
			continue
		}
	}

	if run.Err != "" {
		return run, errors.New(run.Err)
	}

	// Confirmation pass: re-observe and re-plan rather than trust that Apply
	// succeeded. This is what makes partial or drifted application visible
	// instead of silently accepted, and it is exactly what Task 21 gates
	// applied_revision on.
	observed, err = r.ad.Observe(ctx)
	if err != nil {
		run.Err = err.Error()
		return run, fmt.Errorf("post-apply observe: %w", err)
	}
	confirm, err := r.ad.Plan(ctx, desired, observed)
	if err != nil {
		run.Err = err.Error()
		return run, fmt.Errorf("post-apply plan: %w", err)
	}
	run.Converged = confirm.IsEmpty()
	if !run.Converged {
		run.Err = fmt.Sprintf("%d steps still outstanding after apply", len(confirm.Steps))
	}
	return run, nil
}

func (r *Reconciler) applyWithRetry(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	var (
		last    adapter.StepResult
		lastErr error
		backoff = r.opts.RetryBase
	)
	for attempt := 1; attempt <= r.opts.MaxRetries; attempt++ {
		started := r.clk.Now()
		result, err := r.ad.Apply(ctx, step)
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
