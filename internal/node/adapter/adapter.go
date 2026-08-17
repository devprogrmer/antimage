// Package adapter defines the contract every protocol family implements.
//
// The central design point (spec section 4) is that Plan and Apply are
// separate. An adapter is never told HOW to change the host: it receives
// desired state and reports what it would do, tagging each step with the
// disruption that step costs. That is what lets the reconciler debounce
// restarts while applying hot changes immediately, and it is why Xray,
// OpenVPN, and strongSwan can share one reconciler despite completely
// different lifecycles.
//
// This package must not import internal/panel. CI enforces the boundary.
package adapter

import (
	"context"
	"encoding/json"
	"time"
)

// Disruption is the cost of a single step. It belongs to the step, not the
// adapter: adding a user is DisruptNone on all three protocol families, while
// moving a listen port is DisruptRestart on all three.
type Disruption uint8

const (
	// DisruptNone applies without touching running sessions: Xray AddUser
	// over gRPC, appending to chap-secrets, issuing an OpenVPN certificate.
	DisruptNone Disruption = iota
	// DisruptReload re-reads configuration; sessions survive.
	DisruptReload
	// DisruptRestart restarts the service; active sessions drop.
	DisruptRestart
)

func (d Disruption) String() string {
	switch d {
	case DisruptNone:
		return "none"
	case DisruptReload:
		return "reload"
	case DisruptRestart:
		return "restart"
	default:
		return "unknown"
	}
}

type Kind string

type CredentialKind string

const (
	CredUUID     CredentialKind = "uuid"
	CredX509     CredentialKind = "x509"
	CredPassword CredentialKind = "password"
)

// Caps lets the panel and later sub-projects adapt without hardcoding
// protocol knowledge.
type Caps struct {
	HotUserAdd      bool             `json:"hot_user_add"`
	SelfAccounting  bool             `json:"self_accounting"`
	RequiresPKI     bool             `json:"requires_pki"`
	CredentialKinds []CredentialKind `json:"credential_kinds"`
	// ServiceSchema is a JSON Schema describing this adapter's service
	// params. The panel validates writes against it and the UI renders the
	// form from it, so adding a protocol means adding an adapter rather than
	// editing panel code.
	ServiceSchema json.RawMessage `json:"service_schema"`
}

type Descriptor struct {
	Kind    Kind   `json:"kind"`
	Version string `json:"version"`
	Caps    Caps   `json:"caps"`
}

type Credential struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Subject is wired but empty in SP1; SP2 populates it.
type Subject struct {
	ID          int64        `json:"id"`
	Credentials []Credential `json:"credentials"`
}

type Service struct {
	ID      int64           `json:"id"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Params  json.RawMessage `json:"params"`
}

// Desired mirrors the panel's document type field-for-field. It is declared
// here rather than imported so this package stays free of panel code; the
// wire contract keeps the two in sync.
type Desired struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      int64     `json:"revision"`
	NodeID        int64     `json:"node_id"`
	Services      []Service `json:"services"`
	Subjects      []Subject `json:"subjects"`
}

// ObservedService is the adapter's reading of one service on the host.
//
// Managed distinguishes a file antimage wrote from one a human created, and
// Checksum carries the content hash recorded in the file's marker. Together
// they let Plan tell "desired state changed" apart from "somebody edited this
// by hand", which is what makes drift reportable instead of silently
// overwritten.
type ObservedService struct {
	ID       int64
	Present  bool
	Managed  bool
	Checksum string
}

type Observed struct {
	Services []ObservedService
}

type Step struct {
	Seq        int
	Kind       string
	Disruption Disruption
	ServiceID  int64
	Payload    json.RawMessage
}

type Plan struct {
	Steps []Step
}

func (p Plan) IsEmpty() bool { return len(p.Steps) == 0 }

// MaxDisruption reports the worst cost in the plan, which the reconciler uses
// to decide whether a maintenance window applies.
func (p Plan) MaxDisruption() Disruption {
	worst := DisruptNone
	for _, s := range p.Steps {
		if s.Disruption > worst {
			worst = s.Disruption
		}
	}
	return worst
}

type StepResult struct {
	Seq      int
	OK       bool
	Err      string
	Duration time.Duration
}

type Health struct {
	OK     bool
	Detail string
}

// Adapter is implemented once per protocol family.
type Adapter interface {
	// Descriptor returns static identity and capabilities.
	Descriptor() Descriptor

	// Observe reads host truth. It must never mutate anything.
	Observe(ctx context.Context) (Observed, error)

	// Plan diffs desired against observed. It must be pure and repeatable:
	// calling it twice with the same inputs yields the same steps and has no
	// side effects. The convergence property test depends on this.
	Plan(ctx context.Context, desired Desired, observed Observed) (Plan, error)

	// Apply executes exactly one step. Every step must be idempotent, because
	// a retry after a partial failure re-runs it.
	Apply(ctx context.Context, step Step) (StepResult, error)

	// Probe is a cheap liveness check run on the health cadence.
	Probe(ctx context.Context) (Health, error)
}
