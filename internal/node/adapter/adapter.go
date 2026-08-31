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
	"errors"
	"fmt"
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
	CredKeypair  CredentialKind = "keypair" // WireGuard public/private key pair
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

	// SupportsOutbounds and SupportsRouting declare whether this adapter can
	// apply the egress half of a v3 document.
	//
	// Fail closed. An adapter that declares false must never be sent outbounds
	// or routing, because the alternative is a panel that shows an operator an
	// egress policy the node is not enforcing. WireGuard, Hysteria2 and L2TP
	// have no routing engine at all; only the multiplexing proxies do.
	//
	// Declaring true is a promise the contract test enforces.
	SupportsOutbounds bool `json:"supports_outbounds"`
	SupportsRouting   bool `json:"supports_routing"`

	// OutboundSchema is to Outbound.Params what ServiceSchema is to
	// Service.Params. Required when SupportsOutbounds is true.
	OutboundSchema json.RawMessage `json:"outbound_schema,omitempty"`

	// OutboundKinds are the Outbound.Kind values this adapter can render.
	//
	// Published so the UI can offer exactly these and no more. A frontend with
	// its own hardcoded list is the fake feature layer this design exists to
	// prevent: it would let an operator pick a kind the adapter refuses, and a
	// refused outbound is not a validation error on the node -- it is a proxy
	// that will not start, taking every working inbound with it.
	OutboundKinds []string `json:"outbound_kinds,omitempty"`

	// BuiltinOutboundTags are outbounds the adapter provides on its own, which
	// a routing rule may reference without a corresponding panel row.
	//
	// Xray's accounting configuration defines "direct", so a rule may send
	// traffic straight out without anybody creating an outbound for it. The
	// panel needs to know: a rule naming a tag that resolves nowhere makes the
	// adapter refuse to render, which fails Plan for the WHOLE node -- one bad
	// rule would stop it converging on anything, including its inbounds.
	// Publishing the list is what lets the panel refuse that rule at the API
	// instead.
	BuiltinOutboundTags []string `json:"builtin_outbound_tags,omitempty"`
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
// Schema v2 adds enforcement policies.
type Subject struct {
	ID          int64        `json:"id"`
	Credentials []Credential `json:"credentials"`
	// Enforcement policies (schema v2+)
	MaxDevices         *int64 `json:"max_devices,omitempty"`
	MaxIPs             *int64 `json:"max_ips,omitempty"`
	MaxConnections     *int64 `json:"max_connections,omitempty"`
	SpeedLimitUpKbps   *int64 `json:"speed_limit_up_kbps,omitempty"`
	SpeedLimitDownKbps *int64 `json:"speed_limit_down_kbps,omitempty"`
}

type Service struct {
	ID      int64           `json:"id"`
	Kind    string          `json:"kind"`
	Enabled bool            `json:"enabled"`
	Params  json.RawMessage `json:"params"`
}

// MaxSchemaVersion is the highest document version this agent understands.
//
// A document above it is REFUSED, never partially applied. The difference
// matters: the fields a newer version adds are omitempty, so an old agent
// decoding a new document succeeds and silently drops what it did not know
// about. For egress that would mean an operator configuring an outbound, the
// panel reporting convergence, and the node routing traffic somewhere else
// entirely. Refusing is the only safe reading of "I do not understand this".
const MaxSchemaVersion = 3

// Outbound mirrors the panel's Outbound. Schema v3+.
type Outbound struct {
	ID     int64           `json:"id"`
	Tag    string          `json:"tag"`
	Kind   string          `json:"kind"`
	Params json.RawMessage `json:"params"`
}

// RoutingRule mirrors the panel's RoutingRule. Schema v3+.
type RoutingRule struct {
	ID          int64    `json:"id"`
	Priority    int      `json:"priority"`
	Domains     []string `json:"domains,omitempty"`
	IPCIDRs     []string `json:"ip_cidrs,omitempty"`
	GeoIP       []string `json:"geoip,omitempty"`
	GeoSite     []string `json:"geosite,omitempty"`
	Ports       []string `json:"ports,omitempty"`
	Network     string   `json:"network,omitempty"`
	InboundTags []string `json:"inbound_tags,omitempty"`
	SubjectIDs  []int64  `json:"subject_ids,omitempty"`
	OutboundTag string   `json:"outbound_tag"`
}

// Routing mirrors the panel's Routing. Schema v3+.
type Routing struct {
	Rules              []RoutingRule `json:"rules"`
	DefaultOutboundTag string        `json:"default_outbound_tag,omitempty"`
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

	// Egress (schema v3+).
	Outbounds []Outbound `json:"outbounds,omitempty"`
	Routing   *Routing   `json:"routing,omitempty"`
}

// CheckSchemaVersion refuses a document this agent cannot fully apply.
//
// Called before convergence, so an unsupported document produces a reported
// error and no partial apply. A version of zero is treated as unsupported
// rather than defaulted: it means the field was absent, and guessing which
// version a document without one intended is exactly the ambiguity this
// refuses to resolve silently.
func CheckSchemaVersion(v int) error {
	if v <= 0 {
		return fmt.Errorf("desired document carries no schema version")
	}
	if v > MaxSchemaVersion {
		return fmt.Errorf(
			"desired document is schema v%d; this agent understands up to v%d -- upgrade the agent",
			v, MaxSchemaVersion)
	}
	return nil
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
	// Egress is this adapter's reading of the outbound and routing document.
	//
	// Nil means the adapter does not manage egress at all -- either it declares
	// SupportsOutbounds=false, or it has never written one. That is different
	// from a present-but-empty egress document, which is a deliberate statement
	// that the node should route nowhere in particular, and which Plan must be
	// able to tell apart from "never configured" so it does not rewrite a file
	// on every pass.
	Egress *ObservedEgress
}

// ObservedEgress is the adapter's reading of the node's egress configuration.
//
// Managed and Checksum carry the same meaning as on ObservedService: Managed
// separates a file antimage wrote from one a human created, and Checksum is
// computed from what is on disk right now rather than read from the marker, so
// comparing the two is what catches a hand edit.
type ObservedEgress struct {
	Present  bool
	Managed  bool
	Checksum string
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

// StepResult is one step's outcome.
//
// Kind and Disruption are echoed back from the Step that produced this result
// rather than re-derived by the caller. The panel stores both per step
// (node_apply_steps.step_kind / .disruption), and they are the only record an
// operator has of WHAT was done and what it cost: without them a failed run
// reads as "step 3 failed" with no way to tell a hot user add apart from a
// service restart. Adapters need not set them — the reconciler fills them in
// from the step it executed, so the two can never disagree.
type StepResult struct {
	Seq        int
	Kind       string
	Disruption Disruption
	OK         bool
	Err        string
	Duration   time.Duration
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

	// Restart bounces the service ON DEMAND, outside the desired-state
	// reconciliation loop. It exists because "sync" (converge toward the
	// desired document) and "restart" (bounce the running process even
	// though nothing changed) are different operator intents: an operator
	// investigating a wedged process wants the second even when the first
	// would be a no-op.
	//
	// An adapter with no restartable daemon -- nothing today, but the
	// interface has to say what "no" looks like for the ones that might
	// arrive later -- returns ErrRestartUnsupported rather than nil, so a
	// caller cannot mistake "there was nothing to restart" for "the restart
	// happened."
	Restart(ctx context.Context) error
}

// ErrRestartUnsupported is what Adapter.Restart returns for an adapter with
// no restartable daemon of its own. Checked with errors.Is, not a type
// assertion, because a future adapter is free to wrap it with more detail.
var ErrRestartUnsupported = errors.New("restart not supported by this adapter")

// UsageSample is one subject's traffic delta since the last report. SP3
// design decision 1: the agent computes deltas; the panel never sees raw
// cumulative counters.
type UsageSample struct {
	SubjectID int64
	// ServiceID is which service the traffic went through, or 0 when the
	// adapter cannot attribute it (C2).
	//
	// Zero is a real answer, not a failure. An adapter whose counters are
	// per-interface rather than per-inbound genuinely does not know, and
	// forcing it to guess would put a confident wrong number into a bill. The
	// panel stores 0 as NULL and the traffic still counts against the subject.
	ServiceID     int64
	UplinkBytes   uint64
	DownlinkBytes uint64
}

// UsageReporter is an optional adapter capability (SP3 design decision 2).
// Only adapters that can account for themselves implement it; Caps.SelfAccounting
// declares the capability. Type-assert the adapter to this interface to check.
type UsageReporter interface {
	// Usage reads traffic deltas since the last successful call. It is the
	// adapter's responsibility to persist cursors and detect restarts.
	Usage(ctx context.Context) ([]UsageSample, error)
}
