package xray

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Kind is the adapter kind the panel stores in services.adapter_kind.
const Kind adapter.Kind = "xray"

const (
	filePrefix = "antimage-"
	fileSuffix = ".json"
	// markerPrefix begins the first line of every file this adapter writes.
	// It carries the service id and a checksum of the payload, which is what
	// lets Observe tell "we wrote this and it is current" from "we wrote this
	// and somebody edited it" from "a human put this here".
	markerPrefix = "// antimage:"
	// appliedSuffix names the sidecar recording the checksum the RUNTIME was
	// last successfully restarted with. Written only after a restart succeeds.
	//
	// Without it a write that succeeds followed by a restart that fails leaves
	// a correct file on disk, so the next Observe sees no difference and plans
	// nothing -- and the node reports converged while the proxy is still
	// running the old configuration. The file says what should be running; this
	// says what is.
	appliedSuffix = ".applied"
	// statsConfigFile is the shared stats infrastructure (SP3). Written when
	// the adapter has SelfAccounting enabled. Not tied to a service ID.
	statsConfigFile = "antimage-stats.json"
)

// Adapter implements the adapter contract for Xray-core.
type Adapter struct {
	// dir is Xray's confdir. Each service becomes one file in it.
	dir string
	rt  Runtime

	// shapes records, per service, the checksum of the inbound rendered with
	// NO users, as read by the last Observe. Plan compares it against the
	// desired shape to decide whether only membership changed. Written only by
	// Observe, read only by Plan, which the reconciler calls in that order.
	mu     sync.Mutex
	shapes map[int64]string
	// hotAdd mirrors the runtime's capability. Cached at construction because
	// Descriptor is called on every Hello and must not shell out.
	hotAdd bool
}

// New returns an adapter writing into dir and driving rt.
func New(dir string, rt Runtime, hotAdd bool) *Adapter {
	return &Adapter{dir: dir, rt: rt, hotAdd: hotAdd}
}

// serviceSchema is published to the panel, which validates operator input
// against it before a service is ever stored. Keeping it here means adding a
// protocol is an adapter change, not a panel change.
var serviceSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["protocol", "port"],
  "properties": {
    "protocol":  {"type": "string", "enum": ["vless", "vmess", "trojan"]},
    "port":      {"type": "integer", "minimum": 1, "maximum": 65535},
    "listen":    {"type": "string"},
    "network":   {"type": "string", "enum": ["tcp", "ws", "grpc"]},
    "security":  {"type": "string", "enum": ["none", "tls"]},
    "cert_file": {"type": "string"},
    "key_file":  {"type": "string"},
    "sni":       {"type": "string"},
    "path":      {"type": "string"},
    "host":      {"type": "string"},
    "sniffing":  {"type": "boolean"}
  }
}`)

func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		Kind:    Kind,
		Version: "1",
		Caps: adapter.Caps{
			// Declared from the runtime's actual capability, not hardcoded.
			// The panel records this at Hello so the UI can tell an operator
			// BEFORE they click whether adding a user drops sessions.
			HotUserAdd:      a.hotAdd,
			SelfAccounting:  a.hotAdd, // SP3: accounting requires the API inbound
			RequiresPKI:     false,
			CredentialKinds: []adapter.CredentialKind{"uuid", "password"},
			ServiceSchema:   serviceSchema,
		},
	}
}

func (a *Adapter) appliedPath(id int64) string {
	return a.path(id) + appliedSuffix
}

// appliedState is what the RUNTIME is serving, as opposed to what the config
// file on disk says it should serve.
//
// Users is recorded alongside the checksum because a checksum alone cannot
// answer "was anybody removed?", and that question decides between a hot add
// and a restart. Getting it wrong is not a performance problem: a revoked user
// whose removal never reaches the running process stays connected while the
// panel reports them gone.
type appliedState struct {
	Checksum string   `json:"checksum"`
	Users    []string `json:"users"`
}

// recordApplied notes what the runtime is now serving. Called only after the
// runtime has actually accepted it.
func (a *Adapter) recordApplied(serviceID int64, checksum string, users []string) error {
	sorted := append([]string(nil), users...)
	sort.Strings(sorted)
	body, err := json.Marshal(appliedState{Checksum: checksum, Users: sorted})
	if err != nil {
		return fmt.Errorf("encode applied state: %w", err)
	}
	return os.WriteFile(a.appliedPath(serviceID), body, 0o600)
}

// applied reads what the runtime was last successfully restarted with. A zero
// value means "unknown", which forces a restart -- the safe direction.
func (a *Adapter) applied(serviceID int64) appliedState {
	body, err := os.ReadFile(a.appliedPath(serviceID))
	if err != nil {
		return appliedState{}
	}
	var st appliedState
	if err := json.Unmarshal(body, &st); err != nil {
		return appliedState{}
	}
	return st
}

// removedFrom reports which users the runtime is currently serving that the
// desired state no longer contains. A non-empty result disqualifies the hot
// path, because Xray keeps serving a user until it is explicitly told
// otherwise -- deleting them from the config file alone does nothing to an
// established session.
func removedFrom(st appliedState, desired []User) []string {
	want := make(map[string]struct{}, len(desired))
	for _, u := range desired {
		want[u.Email] = struct{}{}
	}
	var gone []string
	for _, email := range st.Users {
		if _, still := want[email]; !still {
			gone = append(gone, email)
		}
	}
	sort.Strings(gone)
	return gone
}

func (a *Adapter) path(id int64) string {
	return filepath.Join(a.dir, filePrefix+strconv.FormatInt(id, 10)+fileSuffix)
}

// marker is the first line of a managed file.
// It carries TWO checksums. sha256 covers the whole payload and catches any
// hand edit. shape covers the inbound rendered with no users at all, which is
// what lets Plan tell "somebody was added" apart from "the port moved" using
// only what Observe can see. Without it the adapter cannot distinguish them
// from a single hash and would classify a port change as a hot user add,
// applying it during business hours and dropping every session on the node.
func marker(serviceID int64, checksum, shape string) string {
	return fmt.Sprintf("%s service=%d sha256=%s shape=%s", markerPrefix, serviceID, checksum, shape)
}

func checksumOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// parseMarker reads the marker line. Returns ok=false for a file this adapter
// did not write, which must never be overwritten.
func parseMarker(body string) (serviceID int64, checksum, shape string, ok bool) {
	line, _, _ := strings.Cut(body, "\n")
	if !strings.HasPrefix(line, markerPrefix) {
		return 0, "", "", false
	}
	for _, field := range strings.Fields(strings.TrimPrefix(line, markerPrefix)) {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		switch key {
		case "service":
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return 0, "", "", false
			}
			serviceID = id
		case "sha256":
			checksum = value
		case "shape":
			shape = value
		}
	}
	return serviceID, checksum, shape, serviceID != 0 && checksum != ""
}

// payloadOf strips the marker line, leaving the config Xray reads. The marker
// is excluded from its own checksum because a line cannot hash itself.
func payloadOf(body string) string {
	_, rest, found := strings.Cut(body, "\n")
	if !found {
		return ""
	}
	return rest
}

// Observe reads host truth and never mutates anything.
//
// A file with no marker is reported Present but not Managed, so Plan leaves it
// alone: a human's config must not be silently overwritten by convergence.
func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
	var obs adapter.Observed
	shapes := map[int64]string{}

	entries, err := os.ReadDir(a.dir)
	if errors.Is(err, os.ErrNotExist) {
		// A node that has never converged has no confdir yet. That is an
		// empty observation, not a failure.
		return obs, nil
	}
	if err != nil {
		return obs, fmt.Errorf("read xray confdir %s: %w", a.dir, err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, filePrefix) ||
			!strings.HasSuffix(name, fileSuffix) || strings.HasSuffix(name, appliedSuffix) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(a.dir, name))
		if err != nil {
			return obs, fmt.Errorf("read %s: %w", name, err)
		}
		body := string(raw)

		id, recorded, shape, ok := parseMarker(body)
		if !ok {
			// Unmanaged: present, but not ours.
			trimmed := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
			if parsed, perr := strconv.ParseInt(trimmed, 10, 64); perr == nil {
				obs.Services = append(obs.Services, adapter.ObservedService{
					ID: parsed, Present: true, Managed: false,
				})
			}
			continue
		}
		// Checksum is computed from what is on disk RIGHT NOW, not taken from
		// the marker: comparing the two is exactly how a hand edit is caught.
		obs.Services = append(obs.Services, adapter.ObservedService{
			ID:       id,
			Present:  true,
			Managed:  true,
			Checksum: checksumOf([]byte(payloadOf(body))),
		})
		shapes[id] = shape
		_ = recorded
	}
	a.mu.Lock()
	a.shapes = shapes
	a.mu.Unlock()

	sort.Slice(obs.Services, func(i, j int) bool { return obs.Services[i].ID < obs.Services[j].ID })
	return obs, nil
}

// stepPayload is what Apply needs to execute a step without re-deriving it.
type stepPayload struct {
	// Config is the rendered inbound, for write steps.
	Config string `json:"config,omitempty"`
	// Tag identifies the inbound for runtime user operations.
	Tag string `json:"tag,omitempty"`
	// User is set on hot add/remove steps.
	Email      string `json:"email,omitempty"`
	Credential string `json:"credential,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	SubjectID  int64  `json:"subject_id,omitempty"`
	// Shape is the checksum of this inbound rendered with no users, recorded
	// in the marker so a later Plan can tell a membership change from a change
	// to the inbound itself.
	Shape string `json:"shape,omitempty"`
	// Users is the tag set this config serves, carried through to the applied
	// sidecar so a later Plan can tell whether anybody is being removed.
	Users []string `json:"users,omitempty"`
	// PolicyConfig is the speed limit policy configuration (enforcement).
	PolicyConfig string `json:"policy_config,omitempty"`
}

// Step kinds. These reach the panel's node_apply_steps.step_kind and are what
// an operator reads when a run fails, so they name the act, not the mechanism.
const (
	StepWriteService  = "write_service"
	StepRemoveService = "remove_service"
	StepAddUser       = "add_user"
	StepRemoveUser    = "remove_user"
	StepValidate      = "validate_config"
	// StepRestartService reapplies a config already on disk that the runtime
	// never successfully loaded.
	StepRestartService = "restart_service"
)

// Plan diffs desired against observed. It is pure and repeatable: it performs
// no I/O beyond what Observe already did and returns the same steps for the
// same inputs, which the convergence property test depends on.
func (a *Adapter) Plan(
	ctx context.Context, desired adapter.Desired, observed adapter.Observed,
) (adapter.Plan, error) {
	var plan adapter.Plan

	present := make(map[int64]adapter.ObservedService, len(observed.Services))
	for _, o := range observed.Services {
		present[o.ID] = o
	}

	// Subjects are node-wide in the document; every enabled service serves
	// every subject granted to it, and the panel has already filtered to the
	// ones entitled here.
	users, err := usersFrom(desired)
	if err != nil {
		return plan, err
	}

	// Generate policy configuration for speed limits (if any subjects have limits)
	var policyConfigData string
	if len(desired.Subjects) > 0 {
		policyBytes, err := GeneratePolicyConfig(desired.Subjects)
		if err != nil {
			return plan, fmt.Errorf("generate policy config: %w", err)
		}
		policyConfigData = string(policyBytes)
	}

	desiredIDs := make(map[int64]struct{}, len(desired.Services))
	seq := 0

	// Services are already ordered by id in the document; iterate in that
	// order so the plan is deterministic.
	for _, svc := range desired.Services {
		if svc.Kind != string(Kind) {
			continue // another adapter owns it
		}
		desiredIDs[svc.ID] = struct{}{}

		if !svc.Enabled {
			// A disabled service must not be serving. Removing its file and
			// restarting is the only honest way to stop it.
			if o, ok := present[svc.ID]; ok && o.Managed {
				seq++
				plan.Steps = append(plan.Steps, adapter.Step{
					Seq: seq, Kind: StepRemoveService,
					Disruption: adapter.DisruptRestart,
					ServiceID:  svc.ID,
				})
			}
			continue
		}

		in, err := ParseInbound(svc.Params)
		if err != nil {
			return adapter.Plan{}, fmt.Errorf("service %d: %w", svc.ID, err)
		}
		rendered, err := in.Generate(users)
		if err != nil {
			return adapter.Plan{}, fmt.Errorf("service %d: %w", svc.ID, err)
		}
		want := checksumOf(rendered)

		st := a.applied(svc.ID)

		o, exists := present[svc.ID]
		switch {
		case !exists:
			// New inbound: a new listener cannot appear without a restart.
			seq++
			plan.Steps = append(plan.Steps,
				a.writeStep(seq, svc.ID, in, rendered, adapter.DisruptRestart, users, policyConfigData))

		case !o.Managed:
			// Somebody else's file occupies our name. Refusing is safer than
			// overwriting: convergence must never destroy an operator's work.
			return adapter.Plan{}, fmt.Errorf(
				"service %d: %s exists but was not written by antimage; refusing to overwrite",
				svc.ID, a.path(svc.ID))

		case o.Checksum == want && st.Checksum != want:
			// The file is right but the runtime never came up with it: a
			// previous restart failed. Restart without rewriting.
			seq++
			plan.Steps = append(plan.Steps, adapter.Step{
				Seq: seq, Kind: StepRestartService,
				Disruption: adapter.DisruptRestart, ServiceID: svc.ID,
				Payload: mustPayload(stepPayload{
					Config: string(rendered), Shape: want, Users: userTags(users), PolicyConfig: policyConfigData,
				}),
			})

		case o.Checksum != want:
			// Content differs. The hot path is only legitimate when the change
			// is PURELY ADDITIVE:
			//
			//   - the inbound's shape is unchanged (a moved port is not a
			//     membership change), and
			//   - the runtime can add users without a restart, and
			//   - nobody is being REMOVED.
			//
			// The last condition is a security boundary, not an optimisation.
			// Xray goes on serving a user until it is told to stop; dropping
			// them from the config file has no effect on an established
			// session. Taking the hot path on a revocation would delete the
			// credential from disk, report the plan converged, and leave the
			// revoked user connected indefinitely.
			//
			// Revocation therefore takes the rewrite-and-restart path, which
			// is the classification recorded in SP2 decision 3.
			revoked := removedFrom(st, users)
			additive := st.Checksum != "" && len(revoked) == 0
			if a.userOnlyChange(svc.ID, in) && a.hotAdd && len(users) > 0 && additive {
				for _, u := range users {
					seq++
					plan.Steps = append(plan.Steps, a.userStep(seq, StepAddUser, svc.ID, in, u))
				}
				// The file still has to match what is running, or the next
				// Observe reports drift forever. Rewriting it costs nothing
				// live because the running instance is already correct.
				seq++
				// DisruptNone: the running instance is already correct because
				// the users were added through the API; the file is brought
				// into line so the next Observe does not report drift.
				plan.Steps = append(plan.Steps,
					a.writeStep(seq, svc.ID, in, rendered, adapter.DisruptNone, users, policyConfigData))
			} else {
				seq++
				plan.Steps = append(plan.Steps,
					a.writeStep(seq, svc.ID, in, rendered, adapter.DisruptRestart, users, policyConfigData))
			}
		}
	}

	// Anything managed that is no longer desired must go.
	var stale []int64
	for id, o := range present {
		if _, wanted := desiredIDs[id]; !wanted && o.Managed {
			stale = append(stale, id)
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i] < stale[j] })
	for _, id := range stale {
		seq++
		plan.Steps = append(plan.Steps, adapter.Step{
			Seq: seq, Kind: StepRemoveService,
			Disruption: adapter.DisruptRestart,
			ServiceID:  id,
		})
	}

	return plan, nil
}

// userDelta reports whether the only difference is the user set, and what
// changed. It re-renders the inbound with the observed user set removed to
// test the hypothesis, which keeps Plan pure.
// userOnlyChange reports whether the only difference between what is running
// and what is desired is the set of users.
//
// It compares the SHAPE checksum recorded in the marker against the shape of
// the desired inbound. A checksum of the whole payload cannot answer this:
// adding a user and moving the port both change it, and treating the second
// as the first would apply a restart-class change as a hot add.
func (a *Adapter) userOnlyChange(serviceID int64, in Inbound) bool {
	a.mu.Lock()
	recorded := a.shapes[serviceID]
	a.mu.Unlock()
	if recorded == "" {
		// Written before shapes were recorded. Be conservative.
		return false
	}
	shell, err := in.Generate(nil)
	if err != nil {
		return false
	}
	return checksumOf(shell) == recorded
}

func (a *Adapter) writeStep(
	seq int, serviceID int64, in Inbound, rendered []byte, d adapter.Disruption, users []User, policyConfig string,
) adapter.Step {
	shell, err := in.Generate(nil)
	shape := ""
	if err == nil {
		shape = checksumOf(shell)
	}
	return adapter.Step{
		Seq:        seq,
		Kind:       StepWriteService,
		Disruption: d,
		ServiceID:  serviceID,
		Payload: mustPayload(stepPayload{
			Config: string(rendered), Shape: shape, Users: userTags(users), PolicyConfig: policyConfig,
		}),
	}
}

// userTags projects users to the tags the runtime knows them by.
func userTags(users []User) []string {
	tags := make([]string, 0, len(users))
	for _, u := range users {
		tags = append(tags, u.Email)
	}
	return tags
}

func (a *Adapter) userStep(seq int, kind string, serviceID int64, in Inbound, u User) adapter.Step {
	return adapter.Step{
		Seq: seq, Kind: kind, Disruption: adapter.DisruptNone, ServiceID: serviceID,
		Payload: mustPayload(stepPayload{
			Tag: in.Tag(), Email: u.Email, Credential: u.Credential,
			Protocol: string(in.Protocol), SubjectID: u.SubjectID,
		}),
	}
}

func mustPayload(p stepPayload) json.RawMessage {
	raw, err := json.Marshal(p)
	if err != nil {
		// stepPayload is a fixed struct of strings and ints; marshalling it
		// cannot fail, and a panic here would be a programming error rather
		// than a runtime condition.
		panic("xray: marshalling a step payload: " + err.Error())
	}
	return raw
}

// usersFrom projects the document's subjects into the per-inbound user list.
func usersFrom(desired adapter.Desired) ([]User, error) {
	users := make([]User, 0, len(desired.Subjects))
	for _, s := range desired.Subjects {
		u := User{SubjectID: s.ID, Email: subjectEmail(s.ID)}
		for _, c := range s.Credentials {
			// Prefer uuid; trojan inbounds pick password at render time.
			if c.Kind == "uuid" {
				u.Credential = c.Value
			}
			if u.Credential == "" && c.Kind == "password" {
				u.Credential = c.Value
			}
		}
		if u.Credential == "" {
			return nil, fmt.Errorf("%w: subject %d has no usable credential", ErrInvalidInbound, s.ID)
		}
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].SubjectID < users[j].SubjectID })
	return users, nil
}

// subjectEmail is the per-user tag inside Xray. It is derived from the subject
// id rather than a name so it is stable across renames, which matters because
// SP3 will aggregate traffic by it.
func subjectEmail(id int64) string {
	return fmt.Sprintf("subject-%d@antimage", id)
}

// Apply executes exactly one step. Every branch is idempotent, because the
// reconciler retries a step after a partial failure.
func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
	started := time.Now()
	result := adapter.StepResult{Seq: step.Seq, Kind: step.Kind, Disruption: step.Disruption}

	fail := func(err error) (adapter.StepResult, error) {
		result.OK = false
		result.Err = err.Error()
		result.Duration = time.Since(started)
		return result, err
	}

	var p stepPayload
	if len(step.Payload) > 0 {
		if err := json.Unmarshal(step.Payload, &p); err != nil {
			return fail(fmt.Errorf("decode step payload: %w", err))
		}
	}

	switch step.Kind {
	case StepWriteService:
		if err := a.writeService(step.ServiceID, []byte(p.Config), p.Shape); err != nil {
			return fail(err)
		}
		// A write that needs a restart is useless until the process reloads.
		if step.Disruption >= adapter.DisruptRestart {
			// Ensure stats config is present before restart (SP3).
			if err := a.ensureStatsConfig(ctx); err != nil {
				return fail(fmt.Errorf("ensure stats config: %w", err))
			}
			// Ensure policy config is present before restart (enforcement).
			if p.PolicyConfig != "" {
				if err := a.ensurePolicyConfig(ctx, []byte(p.PolicyConfig)); err != nil {
					return fail(fmt.Errorf("ensure policy config: %w", err))
				}
			}
			if err := a.rt.Restart(ctx); err != nil {
				return fail(fmt.Errorf("restart after writing service %d: %w", step.ServiceID, err))
			}
		}
		// Recorded only after the runtime is actually serving it.
		if err := a.recordApplied(step.ServiceID, checksumOf([]byte(p.Config)), p.Users); err != nil {
			return fail(fmt.Errorf("record applied state for service %d: %w", step.ServiceID, err))
		}

	case StepRestartService:
		// Ensure stats config is present before restart (SP3).
		if err := a.ensureStatsConfig(ctx); err != nil {
			return fail(fmt.Errorf("ensure stats config: %w", err))
		}
		// Ensure policy config is present before restart (enforcement).
		if p.PolicyConfig != "" {
			if err := a.ensurePolicyConfig(ctx, []byte(p.PolicyConfig)); err != nil {
				return fail(fmt.Errorf("ensure policy config: %w", err))
			}
		}
		if err := a.rt.Restart(ctx); err != nil {
			return fail(fmt.Errorf("restart service %d: %w", step.ServiceID, err))
		}
		if err := a.recordApplied(step.ServiceID, checksumOf([]byte(p.Config)), p.Users); err != nil {
			return fail(fmt.Errorf("record applied state for service %d: %w", step.ServiceID, err))
		}

	case StepRemoveService:
		if err := os.Remove(a.appliedPath(step.ServiceID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(fmt.Errorf("remove applied marker for service %d: %w", step.ServiceID, err))
		}
		if err := os.Remove(a.path(step.ServiceID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Already gone is the state we wanted; anything else is a failure.
			return fail(fmt.Errorf("remove service %d: %w", step.ServiceID, err))
		}
		// Ensure stats config is present before restart (SP3).
		if err := a.ensureStatsConfig(ctx); err != nil {
			return fail(fmt.Errorf("ensure stats config: %w", err))
		}
		// Ensure policy config is present before restart (enforcement).
		if p.PolicyConfig != "" {
			if err := a.ensurePolicyConfig(ctx, []byte(p.PolicyConfig)); err != nil {
				return fail(fmt.Errorf("ensure policy config: %w", err))
			}
		}
		if err := a.rt.Restart(ctx); err != nil {
			return fail(fmt.Errorf("restart after removing service %d: %w", step.ServiceID, err))
		}

	case StepAddUser:
		if err := a.rt.AddUser(ctx, p.Tag, User{
			SubjectID: p.SubjectID, Email: p.Email, Credential: p.Credential,
		}, Protocol(p.Protocol)); err != nil {
			return fail(fmt.Errorf("add user %s to %s: %w", p.Email, p.Tag, err))
		}

	case StepRemoveUser:
		if err := a.rt.RemoveUser(ctx, p.Tag, p.Email); err != nil {
			return fail(fmt.Errorf("remove user %s from %s: %w", p.Email, p.Tag, err))
		}

	default:
		return fail(fmt.Errorf("unknown step kind %q", step.Kind))
	}

	result.OK = true
	result.Duration = time.Since(started)
	return result, nil
}

// writeService writes the marker and payload atomically.
//
// Atomicity matters: a half-written config that Xray reads during a restart
// takes every user on the node offline. Writing to a temp file in the same
// directory and renaming makes the swap atomic on POSIX.
func (a *Adapter) writeService(serviceID int64, rendered []byte, shape string) error {
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("create xray confdir: %w", err)
	}
	body := marker(serviceID, checksumOf(rendered), shape) + "\n" + string(rendered)

	tmp, err := os.CreateTemp(a.dir, filePrefix+"*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := os.Rename(tmpName, a.path(serviceID)); err != nil {
		return fmt.Errorf("install config for service %d: %w", serviceID, err)
	}
	return nil
}

// ensureStatsConfig writes the stats infrastructure file when accounting is
// enabled. It's a separate config document that Xray merges with inbound files.
// Returns true if the file was written or is already current.
func (a *Adapter) ensureStatsConfig(ctx context.Context) error {
	if !a.hotAdd {
		// No API address means no accounting capability.
		return nil
	}
	rt, ok := a.rt.(*ExecRuntime)
	if !ok || rt.APIAddress == "" {
		return nil
	}

	path := filepath.Join(a.dir, statsConfigFile)
	desired, err := GenerateStatsConfig(rt.APIAddress)
	if err != nil {
		return fmt.Errorf("generate stats config: %w", err)
	}
	want := checksumOf(desired)

	// Check if already current.
	existing, err := os.ReadFile(path)
	if err == nil && checksumOf(existing) == want {
		return nil
	}

	// Write atomically.
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("create xray confdir: %w", err)
	}
	tmp, err := os.CreateTemp(a.dir, "stats-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp stats config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(desired); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp stats config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp stats config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp stats config: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp stats config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install stats config: %w", err)
	}
	return nil
}

// Probe is the cheap liveness check on the health cadence.
func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	if err := a.rt.Available(ctx); err != nil {
		return adapter.Health{OK: false, Detail: err.Error()}, nil
	}
	ok, detail := a.rt.Healthy(ctx)
	return adapter.Health{OK: ok, Detail: detail}, nil
}

// compile-time proof that the contract is satisfied.
var _ adapter.Adapter = (*Adapter)(nil)
