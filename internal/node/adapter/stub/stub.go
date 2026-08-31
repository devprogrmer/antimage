// Package stub is a working adapter that manages plain files.
//
// It exists so SP1 can exercise the whole contract — atomic writes, ownership
// markers, checksums, drift detection, idempotent steps — without requiring
// Xray, OpenVPN, or strongSwan on the host. SP5 and SP6 follow its shape.
package stub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

const (
	Kind         = adapter.Kind("stub")
	MarkerPrefix = "# antimage-managed"
	filePrefix   = "service-"
	fileSuffix   = ".conf"
)

type Adapter struct {
	root string
}

func New(root string) *Adapter { return &Adapter{root: root} }

func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		Kind:    Kind,
		Version: "1",
		Caps: adapter.Caps{
			HotUserAdd:      true,
			SelfAccounting:  false,
			RequiresPKI:     false,
			CredentialKinds: []adapter.CredentialKind{adapter.CredUUID},
			ServiceSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["port"],
  "properties": {
    "port": {"type": "integer", "minimum": 1, "maximum": 65535}
  }
}`),
		},
	}
}

func (a *Adapter) path(id int64) string {
	return filepath.Join(a.root, filePrefix+strconv.FormatInt(id, 10)+fileSuffix)
}

// payloadOf builds the part of the managed file that carries the service's
// actual state, excluding the marker line. It is deliberately excluded from
// its own checksum: a line cannot hash itself.
func payloadOf(svc adapter.Service) string {
	return fmt.Sprintf("id=%d\nenabled=%t\nparams=%s\n",
		svc.ID, svc.Enabled, string(svc.Params))
}

func checksumOf(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// render builds the managed file body: a marker line carrying the payload's
// checksum (for a human or auditor reading the file), then the payload.
func render(svc adapter.Service) string {
	payload := payloadOf(svc)
	return fmt.Sprintf("%s sha256=%s\n%s", MarkerPrefix, checksumOf(payload), payload)
}

// isManaged reports whether antimage wrote this file: it carries our marker.
func isManaged(body string) bool {
	return strings.HasPrefix(body, MarkerPrefix)
}

func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
	entries, err := os.ReadDir(a.root)
	if os.IsNotExist(err) {
		return adapter.Observed{}, nil
	}
	if err != nil {
		return adapter.Observed{}, fmt.Errorf("read %s: %w", a.root, err)
	}

	var out []adapter.ObservedService
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
			continue
		}
		idPart := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
		id, err := strconv.ParseInt(idPart, 10, 64)
		if err != nil {
			continue // not ours; leave it alone
		}
		body, err := os.ReadFile(filepath.Join(a.root, name))
		if err != nil {
			return adapter.Observed{}, fmt.Errorf("read %s: %w", name, err)
		}
		text := string(body)
		managed := isManaged(text)

		// Checksum is computed fresh from what is actually on disk right now
		// — never trust the value a file claims about itself in its own
		// marker line. This is what lets Plan tell converged state apart
		// from drift without re-reading the filesystem itself: Plan only
		// ever sees this field, so if a hand edit changes the bytes on
		// disk, it must change here too, in the one place that reads disk.
		_, payload, _ := strings.Cut(text, "\n")
		checksum := checksumOf(payload)

		out = append(out, adapter.ObservedService{
			ID: id, Present: true, Managed: managed, Checksum: checksum,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return adapter.Observed{Services: out}, nil
}

func (a *Adapter) Plan(ctx context.Context, desired adapter.Desired, observed adapter.Observed) (adapter.Plan, error) {
	seen := map[int64]adapter.ObservedService{}
	for _, o := range observed.Services {
		seen[o.ID] = o
	}

	var steps []adapter.Step
	next := func() int { return len(steps) + 1 }

	// Desired services sorted by id, so plans are deterministic.
	services := append([]adapter.Service(nil), desired.Services...)
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })

	for _, svc := range services {
		wantChecksum := checksumOf(payloadOf(svc))

		obs, present := seen[svc.ID]
		if present && obs.Managed {
			// Compare desired against what Observe already read from disk.
			// Plan takes no filesystem input of its own: everything it
			// knows about host state comes through observed, which is what
			// keeps it pure and repeatable for the same (desired, observed)
			// pair.
			if obs.Checksum == wantChecksum {
				continue // converged
			}
		}
		if present && !obs.Managed {
			// Never overwrite a file we did not create — but say so. A
			// silently-skipped step here would let this service's desired
			// state go unapplied while the plan still reports converged,
			// which is exactly the class of silent failure the Plan/Apply
			// split exists to surface. Emitting a step that Apply then
			// fails keeps this service's blockage visible and diagnosable
			// without holding up any other service's convergence.
			steps = append(steps, adapter.Step{
				Seq:        next(),
				Kind:       "blocked_unmanaged",
				Disruption: adapter.DisruptNone, // nothing will be done at all
				ServiceID:  svc.ID,
			})
			continue
		}

		payload, err := json.Marshal(map[string]any{"body": render(svc)})
		if err != nil {
			return adapter.Plan{}, fmt.Errorf("encode step payload: %w", err)
		}
		steps = append(steps, adapter.Step{
			Seq:        next(),
			Kind:       "write_service",
			Disruption: adapter.DisruptRestart, // params changes move a listen port
			ServiceID:  svc.ID,
			Payload:    payload,
		})
	}

	// Anything managed that is no longer desired gets removed.
	desiredIDs := map[int64]bool{}
	for _, s := range services {
		desiredIDs[s.ID] = true
	}
	for _, o := range observed.Services {
		if desiredIDs[o.ID] || !o.Managed {
			continue
		}
		steps = append(steps, adapter.Step{
			Seq:        next(),
			Kind:       "remove_service",
			Disruption: adapter.DisruptRestart,
			ServiceID:  o.ID,
		})
	}

	return adapter.Plan{Steps: steps}, nil
}

func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	fail := func(err error) (adapter.StepResult, error) {
		return adapter.StepResult{Seq: step.Seq, OK: false, Err: err.Error()}, err
	}

	switch step.Kind {
	case "write_service":
		var p struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(step.Payload, &p); err != nil {
			return fail(fmt.Errorf("decode payload: %w", err))
		}
		if err := os.MkdirAll(a.root, 0o700); err != nil {
			return fail(fmt.Errorf("create root: %w", err))
		}
		if err := atomicWrite(a.path(step.ServiceID), []byte(p.Body)); err != nil {
			return fail(err)
		}

	case "remove_service":
		if err := os.Remove(a.path(step.ServiceID)); err != nil && !os.IsNotExist(err) {
			return fail(fmt.Errorf("remove service %d: %w", step.ServiceID, err))
		}

	case "blocked_unmanaged":
		// Never write here — the file at this path exists and was not
		// created by antimage. Fail the step so the caller sees this
		// service as unconverged, instead of silently doing nothing and
		// letting the plan appear to have succeeded.
		return fail(fmt.Errorf(
			"service %d: refusing to write %s: file exists but was not created by antimage; "+
				"remove it or adopt it (add the antimage marker) to let this service converge",
			step.ServiceID, a.path(step.ServiceID)))

	default:
		return fail(fmt.Errorf("unknown step kind %q", step.Kind))
	}

	return adapter.StepResult{Seq: step.Seq, OK: true}, nil
}

// atomicWrite writes to a temporary file in the same directory and renames
// it into place, so a crash mid-write can never leave a truncated config
// behind.
//
// Durability: the temp file's contents are fsynced before the rename, so
// the bytes are on disk by the time the rename is issued. After a
// successful rename, the containing directory is fsynced too, on platforms
// that support it, so the directory entry pointing at those bytes survives
// a crash as well — without that second sync, a crash between rename and
// the next directory metadata flush can, on some filesystems and journaling
// modes, leave the rename undone even though the data itself is durable.
func atomicWrite(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".antimage-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}

	// Best effort: the write has already succeeded at this point. Opening
	// or syncing a directory handle is not supported on Windows at all, and
	// even on platforms that do support it, a failed directory sync does
	// not invalidate the rename that already landed — it only widens the
	// crash window for the directory entry, which the reconciler's next
	// Observe/Plan/Apply cycle would self-heal regardless. So neither error
	// here is allowed to turn a successful config write into a reported
	// failure.
	if dirFile, err := os.Open(dir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}

	return nil
}

func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	if err := os.MkdirAll(a.root, 0o700); err != nil {
		return adapter.Health{OK: false, Detail: err.Error()}, nil
	}
	return adapter.Health{OK: true, Detail: "stub adapter ready"}, nil
}

// Restart is unsupported: this adapter manages plain files directly, with
// no daemon or unit behind them to bounce.
func (a *Adapter) Restart(ctx context.Context) error {
	return adapter.ErrRestartUnsupported
}
