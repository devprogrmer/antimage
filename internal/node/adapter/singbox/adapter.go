package singbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Kind is the adapter kind the panel stores in services.adapter_kind.
const Kind adapter.Kind = "singbox"

const (
	filePrefix = "antimage-"
	fileSuffix = ".json"
	// markerFile holds the marker for a service, because sing-box parses every
	// .json in its config directory strictly and would reject a comment line.
	// Xray tolerates a leading // comment; sing-box does not, so the marker
	// lives beside the config rather than inside it.
	markerSuffix = ".marker"
	// appliedSuffix names the sidecar recording the checksum the RUNTIME was
	// last successfully restarted with. Written only after a restart succeeds.
	//
	// Without it, a write that succeeds followed by a restart that fails leaves
	// a correct file on disk, so the next Observe sees no drift and plans
	// nothing -- and the node reports converged while sing-box is still serving
	// the old configuration, including users who have since been revoked. The
	// config file says what should be running; this says what is.
	appliedSuffix = ".applied"
)

// ErrRuntimeUnavailable means sing-box or its unit could not be reached.
var ErrRuntimeUnavailable = errors.New("sing-box runtime unavailable")

// Runtime is the adapter's contact with the sing-box process.
//
// There is deliberately no AddUser: sing-box exposes no stable management API
// for user mutation, and providing a method that secretly restarted would make
// the disruption classification a lie.
type Runtime interface {
	Available(ctx context.Context) error
	Restart(ctx context.Context) error
	Healthy(ctx context.Context) (bool, string)
}

// ExecRuntime drives sing-box through systemd.
type ExecRuntime struct {
	Unit           string
	Binary         string
	CommandTimeout time.Duration
}

func NewExecRuntime(unit, binary string) *ExecRuntime {
	if unit == "" {
		unit = "sing-box"
	}
	if binary == "" {
		binary = "sing-box"
	}
	return &ExecRuntime{Unit: unit, Binary: binary, CommandTimeout: 30 * time.Second}
}

func (r *ExecRuntime) timeout() time.Duration {
	if r.CommandTimeout <= 0 {
		return 30 * time.Second
	}
	return r.CommandTimeout
}

func (r *ExecRuntime) run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (r *ExecRuntime) Available(_ context.Context) error {
	if _, err := exec.LookPath(r.Binary); err != nil {
		return fmt.Errorf("%w: %s not found in PATH: %w", ErrRuntimeUnavailable, r.Binary, err)
	}
	return nil
}

func (r *ExecRuntime) Restart(ctx context.Context) error {
	_, err := r.run(ctx, "systemctl", "restart", r.Unit)
	return err
}

func (r *ExecRuntime) Healthy(ctx context.Context) (bool, string) {
	out, err := r.run(ctx, "systemctl", "is-active", r.Unit)
	state := strings.TrimSpace(out)
	if err != nil || state != "active" {
		return false, fmt.Sprintf("%s is %s", r.Unit, state)
	}
	return true, "active"
}

// Adapter implements the adapter contract for sing-box.
type Adapter struct {
	dir string
	rt  Runtime
}

func New(dir string, rt Runtime) *Adapter { return &Adapter{dir: dir, rt: rt} }

// outboundSchema validates Outbound.Params, as serviceSchema validates
// Service.Params. Identical in shape to the Xray adapter's, because the
// document's outbound vocabulary is the panel's rather than any one proxy's --
// the per-proxy differences are handled when rendering, not when validating.
var outboundSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "address":         {"type": "string"},
    "port":            {"type": "integer", "minimum": 1, "maximum": 65535},
    "username":        {"type": "string"},
    "password":        {"type": "string", "writeOnly": true},
    "private_key":     {"type": "string", "writeOnly": true},
    "peer_public_key": {"type": "string"},
    "endpoint":        {"type": "string"},
    "local_addresses": {"type": "array", "items": {"type": "string"}},
    "mtu":             {"type": "integer", "minimum": 576, "maximum": 9000}
  }
}`)

// OutboundKinds is the set of Outbound.Kind values this adapter can render.
var OutboundKinds = []string{"direct", "block", "socks", "http", "wireguard"}

var serviceSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["protocol", "port"],
  "properties": {
    "protocol":  {"type": "string", "enum": ["vless", "vmess", "trojan", "shadowsocks"]},
    "port":      {"type": "integer", "minimum": 1, "maximum": 65535},
    "listen":    {"type": "string"},
    "network":   {"type": "string", "enum": ["tcp", "ws"]},
    "tls":       {"type": "boolean"},
    "cert_file": {"type": "string"},
    "key_file":  {"type": "string"},
    "sni":       {"type": "string"},
    "path":      {"type": "string"},
    "host":      {"type": "string"},
    "method":    {"type": "string", "enum": ["aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305"]},
    "sniff":     {"type": "boolean"}
  }
}`)

func (a *Adapter) Descriptor() adapter.Descriptor {
	return adapter.Descriptor{
		Kind:    Kind,
		Version: "1",
		Caps: adapter.Caps{
			// FALSE, and this is the whole point of the capability. sing-box
			// has no stable API for mutating users on a running instance, so
			// every membership change restarts the process. The panel records
			// this at Hello so an operator is told before they click.
			HotUserAdd:      false,
			SelfAccounting:  false,
			RequiresPKI:     false,
			CredentialKinds: []adapter.CredentialKind{"uuid", "password"},
			ServiceSchema:   serviceSchema,
			// sing-box has a full route block with named outbounds, so it can
			// apply the egress half of a v3 document.
			SupportsOutbounds: true,
			SupportsRouting:   true,
			OutboundSchema:    outboundSchema,
			OutboundKinds:     OutboundKinds,
		},
	}
}

func (a *Adapter) path(id int64) string {
	return filepath.Join(a.dir, filePrefix+strconv.FormatInt(id, 10)+fileSuffix)
}

func (a *Adapter) markerPath(id int64) string {
	return a.path(id) + markerSuffix
}

func (a *Adapter) appliedPath(id int64) string {
	return a.path(id) + appliedSuffix
}

// recordApplied notes the checksum the runtime is now serving. Called only
// after a restart has actually succeeded.
func (a *Adapter) recordApplied(serviceID int64, checksum string) error {
	return os.WriteFile(a.appliedPath(serviceID), []byte(checksum+"\n"), 0o600)
}

// appliedChecksum reads what the runtime was last successfully restarted with.
// An empty string means "never applied", which forces a restart.
func (a *Adapter) appliedChecksum(serviceID int64) string {
	body, err := os.ReadFile(a.appliedPath(serviceID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func checksumOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Observe reads host truth without mutating anything.
//
// A config with no sidecar marker is Present but not Managed: sing-box's own
// config, or a human's, must never be silently overwritten.
func (a *Adapter) Observe(_ context.Context) (adapter.Observed, error) {
	var obs adapter.Observed

	entries, err := os.ReadDir(a.dir)
	if errors.Is(err, os.ErrNotExist) {
		return obs, nil
	}
	if err != nil {
		return obs, fmt.Errorf("read sing-box config dir %s: %w", a.dir, err)
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, filePrefix) ||
			!strings.HasSuffix(name, fileSuffix) || strings.HasSuffix(name, markerSuffix) {
			continue
		}
		// Egress is node-scoped and carries no service id, so it must be read
		// before the numeric parse below -- which would fail on "egress" and
		// skip the file, leaving a hand edit to the routing table permanently
		// invisible.
		if name == egressFile {
			body, err := os.ReadFile(filepath.Join(a.dir, name))
			if err != nil {
				return obs, fmt.Errorf("read %s: %w", name, err)
			}
			eg := adapter.ObservedEgress{Present: true}
			if _, err := os.Stat(a.egressMarkerPath()); err == nil {
				eg.Managed = true
				// From disk, not from the marker: comparing the two is what
				// catches an edit.
				eg.Checksum = checksumOf(body)
			}
			obs.Egress = &eg
			continue
		}

		trimmed := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
		id, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			continue
		}
		body, err := os.ReadFile(filepath.Join(a.dir, name))
		if err != nil {
			return obs, fmt.Errorf("read %s: %w", name, err)
		}

		svc := adapter.ObservedService{ID: id, Present: true}
		if _, err := os.Stat(a.markerPath(id)); err == nil {
			svc.Managed = true
			// Computed from what is on disk now, so a hand edit is caught.
			svc.Checksum = checksumOf(body)
		}
		obs.Services = append(obs.Services, svc)
	}

	sort.Slice(obs.Services, func(i, j int) bool { return obs.Services[i].ID < obs.Services[j].ID })
	return obs, nil
}

type stepPayload struct {
	Config string `json:"config,omitempty"`
}

const (
	StepWriteService  = "write_service"
	StepRemoveService = "remove_service"
	// StepRestartService reapplies a config already on disk that the runtime
	// never successfully loaded.
	StepRestartService = "restart_service"
	// Egress steps are node-scoped: no ServiceID, because outbounds and
	// routing belong to the node rather than to any one inbound.
	StepWriteEgress  = "write_egress"
	StepRemoveEgress = "remove_egress"
)

// Plan diffs desired against observed. Pure and repeatable.
//
// Every change is DisruptRestart, including adding one user. That is not
// laziness: without a management API the only way to make a new user real is
// to rewrite the config and restart, and classifying it as anything cheaper
// would let the reconciler apply it inside a maintenance window it was never
// granted.
func (a *Adapter) Plan(
	_ context.Context, desired adapter.Desired, observed adapter.Observed,
) (adapter.Plan, error) {
	var plan adapter.Plan

	present := make(map[int64]adapter.ObservedService, len(observed.Services))
	for _, o := range observed.Services {
		present[o.ID] = o
	}

	users, err := usersFrom(desired)
	if err != nil {
		return plan, err
	}

	desiredIDs := make(map[int64]struct{}, len(desired.Services))
	seq := 0

	for _, svc := range desired.Services {
		if svc.Kind != string(Kind) {
			continue
		}
		desiredIDs[svc.ID] = struct{}{}

		if !svc.Enabled {
			if o, ok := present[svc.ID]; ok && o.Managed {
				seq++
				plan.Steps = append(plan.Steps, adapter.Step{
					Seq: seq, Kind: StepRemoveService,
					Disruption: adapter.DisruptRestart, ServiceID: svc.ID,
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

		o, exists := present[svc.ID]
		switch {
		case exists && !o.Managed:
			return adapter.Plan{}, fmt.Errorf(
				"service %d: %s exists but was not written by antimage; refusing to overwrite",
				svc.ID, a.path(svc.ID))
		case !exists || o.Checksum != want:
			seq++
			plan.Steps = append(plan.Steps, adapter.Step{
				Seq: seq, Kind: StepWriteService,
				Disruption: adapter.DisruptRestart, ServiceID: svc.ID,
				Payload: mustPayload(stepPayload{Config: string(rendered)}),
			})
		case a.appliedChecksum(svc.ID) != want:
			// The file is right but the runtime never came up with it: an
			// earlier restart failed. Restart without rewriting, rather than
			// reporting a convergence that never reached the process.
			seq++
			plan.Steps = append(plan.Steps, adapter.Step{
				Seq: seq, Kind: StepRestartService,
				Disruption: adapter.DisruptRestart, ServiceID: svc.ID,
				Payload: mustPayload(stepPayload{Config: string(rendered)}),
			})
		}
	}

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
			Disruption: adapter.DisruptRestart, ServiceID: id,
		})
	}

	egressStep, err := a.planEgress(desired, observed, seq)
	if err != nil {
		return adapter.Plan{}, err
	}
	if egressStep != nil {
		plan.Steps = append(plan.Steps, *egressStep)
	}

	return plan, nil
}

func mustPayload(p stepPayload) json.RawMessage {
	raw, err := json.Marshal(p)
	if err != nil {
		panic("singbox: marshalling a step payload: " + err.Error())
	}
	return raw
}

func usersFrom(desired adapter.Desired) ([]User, error) {
	users := make([]User, 0, len(desired.Subjects))
	for _, s := range desired.Subjects {
		u := User{SubjectID: s.ID, Name: subjectName(s.ID)}
		for _, c := range s.Credentials {
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

// subjectName is the per-user tag inside sing-box, derived from the subject id
// so it survives renames. SP3 will aggregate traffic by it.
func subjectName(id int64) string {
	return fmt.Sprintf("subject-%d", id)
}

// Apply executes one step. Every branch is idempotent.
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
		if err := a.writeService(step.ServiceID, []byte(p.Config)); err != nil {
			return fail(err)
		}
		if err := a.rt.Restart(ctx); err != nil {
			return fail(fmt.Errorf("restart after writing service %d: %w", step.ServiceID, err))
		}
		// Recorded only after the runtime is actually serving it.
		if err := a.recordApplied(step.ServiceID, checksumOf([]byte(p.Config))); err != nil {
			return fail(fmt.Errorf("record applied state for service %d: %w", step.ServiceID, err))
		}

	case StepWriteEgress:
		if err := a.writeEgress([]byte(p.Config)); err != nil {
			return fail(err)
		}
		// sing-box reads outbounds and routing only at startup. Without the
		// restart the file is correct and the running process still routes by
		// the previous table -- converged on disk, wrong in memory.
		if err := a.rt.Restart(ctx); err != nil {
			return fail(fmt.Errorf("restart after writing egress: %w", err))
		}

	case StepRemoveEgress:
		if err := a.removeEgress(); err != nil {
			return fail(err)
		}
		if err := a.rt.Restart(ctx); err != nil {
			return fail(fmt.Errorf("restart after removing egress: %w", err))
		}

	case StepRestartService:
		if err := a.rt.Restart(ctx); err != nil {
			return fail(fmt.Errorf("restart service %d: %w", step.ServiceID, err))
		}
		if err := a.recordApplied(step.ServiceID, checksumOf([]byte(p.Config))); err != nil {
			return fail(fmt.Errorf("record applied state for service %d: %w", step.ServiceID, err))
		}

	case StepRemoveService:
		for _, path := range []string{
			a.path(step.ServiceID), a.markerPath(step.ServiceID), a.appliedPath(step.ServiceID),
		} {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fail(fmt.Errorf("remove %s: %w", path, err))
			}
		}
		if err := a.rt.Restart(ctx); err != nil {
			return fail(fmt.Errorf("restart after removing service %d: %w", step.ServiceID, err))
		}

	default:
		return fail(fmt.Errorf("unknown step kind %q", step.Kind))
	}

	result.OK = true
	result.Duration = time.Since(started)
	return result, nil
}

// writeService writes config and marker atomically.
//
// The config is written first and the marker second: if the process dies
// between them, Observe sees an unmanaged file and refuses to touch it, which
// is loud. The reverse order would leave a marker claiming ownership of a file
// that does not exist.
func (a *Adapter) writeService(serviceID int64, rendered []byte) error {
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("create sing-box config dir: %w", err)
	}
	if err := writeFileAtomic(a.dir, a.path(serviceID), rendered); err != nil {
		return err
	}
	return writeFileAtomic(a.dir, a.markerPath(serviceID), []byte(checksumOf(rendered)+"\n"))
}

func writeFileAtomic(dir, path string, body []byte) error {
	tmp, err := os.CreateTemp(dir, filePrefix+"*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	// Configs carry credentials in plaintext; that is what sing-box reads.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}

func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	if err := a.rt.Available(ctx); err != nil {
		return adapter.Health{OK: false, Detail: err.Error()}, nil
	}
	ok, detail := a.rt.Healthy(ctx)
	return adapter.Health{OK: ok, Detail: detail}, nil
}

var _ adapter.Adapter = (*Adapter)(nil)

// egressPath is the node-scoped outbound and routing document.
func (a *Adapter) egressPath() string { return filepath.Join(a.dir, egressFile) }

// egressMarkerPath is the sidecar proving this adapter wrote the egress file.
//
// A sidecar rather than a marker line, for the same reason service files use
// one: sing-box parses every .json in its config directory strictly and would
// reject a leading comment.
func (a *Adapter) egressMarkerPath() string { return a.egressPath() + markerSuffix }

// planEgress diffs the node's outbound and routing document.
//
// Every outcome is restart-class. sing-box reads its config directory once at
// startup and exposes no management API for outbounds or routing, so a change
// here is only live after the process restarts.
func (a *Adapter) planEgress(
	desired adapter.Desired, observed adapter.Observed, seq int,
) (*adapter.Step, error) {
	rendered, err := GenerateEgressConfig(desired.Outbounds, desired.Routing)
	if err != nil {
		return nil, fmt.Errorf("generate egress config: %w", err)
	}

	if rendered == nil {
		if observed.Egress != nil && observed.Egress.Present && observed.Egress.Managed {
			return &adapter.Step{
				Seq: seq + 1, Kind: StepRemoveEgress,
				Disruption: adapter.DisruptRestart,
			}, nil
		}
		return nil, nil
	}

	want := checksumOf(rendered)

	if observed.Egress != nil && observed.Egress.Present {
		if !observed.Egress.Managed {
			return nil, fmt.Errorf(
				"%s exists but was not written by antimage; refusing to overwrite it. "+
					"Move it aside to let the panel manage egress on this node", egressFile)
		}
		if observed.Egress.Checksum == want {
			return nil, nil // converged
		}
	}

	return &adapter.Step{
		Seq: seq + 1, Kind: StepWriteEgress,
		Disruption: adapter.DisruptRestart,
		Payload:    mustPayload(stepPayload{Config: string(rendered)}),
	}, nil
}

// writeEgress installs the egress document and its marker atomically.
func (a *Adapter) writeEgress(rendered []byte) error {
	if err := os.MkdirAll(a.dir, 0o700); err != nil {
		return fmt.Errorf("create sing-box config dir: %w", err)
	}
	if err := writeFileAtomic(a.dir, a.egressPath(), rendered); err != nil {
		return err
	}
	return writeFileAtomic(a.dir, a.egressMarkerPath(), []byte(checksumOf(rendered)+"\n"))
}

// removeEgress drops the document and its marker.
//
// The marker goes too: leaving it behind would make a later hand-written
// egress file look managed, and this adapter would then overwrite something a
// human put there.
func (a *Adapter) removeEgress() error {
	if err := os.Remove(a.egressPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", egressFile, err)
	}
	if err := os.Remove(a.egressMarkerPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove egress marker: %w", err)
	}
	return nil
}
