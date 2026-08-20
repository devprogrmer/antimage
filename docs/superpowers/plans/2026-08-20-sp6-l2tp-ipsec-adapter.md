# antimage SP6 — L2TP/IPsec Adapter Implementation Plan

**Goal:** Implement the L2TP/IPsec adapter per the adapter contract, integrating strongSwan, xl2tpd, PPP secrets management, and per-IP nftables accounting.

**Spec:** `docs/superpowers/specs/2026-08-20-sp6-l2tp-ipsec-adapter.md`. Read it before Phase A.

**Dependencies:** SP1 (adapter contract), SP2 (subjects/credentials), SP3 (accounting), SP5 (adapter registry)

---

## Global Constraints

Every phase's requirements implicitly include this section.

- **Adapter contract compliance:** Must implement all five interface methods
- **No `internal/panel` imports:** Adapter lives in `internal/node/adapter/l2tp/`
- **Ownership markers:** Every managed file starts with `# antimage-managed: service_id=X checksum=Y`
- **Atomic writes:** temp file + rename pattern (never partial writes)
- **Idempotent Apply:** Re-running a step with the same input produces the same result
- **Disruption accuracy:** Users = DisruptReload, params = DisruptRestart
- **External accounting:** nftables counters, not self-reporting (SelfAccounting=false)
- **Password credential kind:** Use existing `adapter.CredPassword` from SP2
- **One service per node:** Adapter enforces single L2TP service limit
- **Test after every phase:** Run `go test ./internal/node/adapter/l2tp/...` before proceeding

---

## Implementation Order

| Phase | Delivers | Verification |
|---|---|---|
| **A** | Adapter skeleton, registration, schema | Compiles, registers in agent |
| **B** | Config generation (strongSwan, xl2tpd, PPP) | Unit tests pass, renders valid configs |
| **C** | Observe/Plan/Apply/Probe implementation | Integration test: Observe→Plan→Apply cycle |
| **D** | nftables accounting, UsageReporter | Unit tests, accounting deltas computed |
| **E** | End-to-end verification, cleanup | All tests pass, manual verification |

---

## Phase A — Adapter Skeleton and Registration

**Goal:** Create the L2TP adapter structure, define the service schema, and register it in the adapter registry.

### Task A1: Create adapter package structure

**Files:**
- Create: `internal/node/adapter/l2tp/adapter.go`
- Create: `internal/node/adapter/l2tp/adapter_test.go`

**Implementation:**

```go
// internal/node/adapter/l2tp/adapter.go
package l2tp

import (
    "context"
    "encoding/json"
    
    "github.com/amyrm/antimage/internal/node/adapter"
)

const Kind adapter.Kind = "l2tp"

type Adapter struct {
    // confDir is where we write strongSwan/xl2tpd configs
    confDir string
    // stateDir is where we persist accounting cursors
    stateDir string
}

func New(confDir, stateDir string) *Adapter {
    return &Adapter{
        confDir:  confDir,
        stateDir: stateDir,
    }
}

var serviceSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["ip_range", "local_ip", "psk"],
  "properties": {
    "ip_range": {
      "type": "string",
      "pattern": "^[0-9.]+\\-[0-9.]+$",
      "description": "Client IP range (e.g., 10.8.0.2-10.8.0.254)"
    },
    "local_ip": {
      "type": "string",
      "format": "ipv4",
      "description": "Server local IP for L2TP (e.g., 10.8.0.1)"
    },
    "psk": {
      "type": "string",
      "minLength": 16,
      "description": "Pre-shared key for IPsec"
    },
    "dns_servers": {
      "type": "array",
      "items": {"type": "string", "format": "ipv4"},
      "description": "DNS servers pushed to clients"
    }
  }
}`)

func (a *Adapter) Descriptor() adapter.Descriptor {
    return adapter.Descriptor{
        Kind:    Kind,
        Version: "1",
        Caps: adapter.Caps{
            HotUserAdd:      true,  // CHAP reload + swanctl --load-creds
            SelfAccounting:  false, // Uses external nftables
            RequiresPKI:     false,
            CredentialKinds: []adapter.CredentialKind{adapter.CredPassword},
            ServiceSchema:   serviceSchema,
        },
    }
}

func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
    // TODO: Phase C
    return adapter.Observed{}, nil
}

func (a *Adapter) Plan(ctx context.Context, desired adapter.Desired, observed adapter.Observed) (adapter.Plan, error) {
    // TODO: Phase C
    return adapter.Plan{}, nil
}

func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
    // TODO: Phase C
    return adapter.StepResult{}, nil
}

func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
    // TODO: Phase C
    return adapter.Health{OK: true}, nil
}
```

**Test:**

```go
// internal/node/adapter/l2tp/adapter_test.go
package l2tp

import (
    "encoding/json"
    "testing"
    
    "github.com/amyrm/antimage/internal/node/adapter"
)

func TestDescriptor(t *testing.T) {
    a := New("/tmp/l2tp-test", "/tmp/l2tp-state")
    desc := a.Descriptor()
    
    if desc.Kind != Kind {
        t.Errorf("want kind %q, got %q", Kind, desc.Kind)
    }
    if desc.Caps.HotUserAdd != true {
        t.Error("want HotUserAdd=true")
    }
    if desc.Caps.SelfAccounting != false {
        t.Error("want SelfAccounting=false")
    }
    if len(desc.Caps.CredentialKinds) != 1 || desc.Caps.CredentialKinds[0] != adapter.CredPassword {
        t.Error("want CredentialKinds=[password]")
    }
    
    // Verify schema is valid JSON
    var schema map[string]interface{}
    if err := json.Unmarshal(desc.Caps.ServiceSchema, &schema); err != nil {
        t.Fatalf("invalid service schema: %v", err)
    }
}
```

**Verification:**

```bash
go test ./internal/node/adapter/l2tp/...
```

---

### Task A2: Register L2TP adapter in registry

**Files:**
- Modify: `internal/node/agent/agent.go` (or wherever adapters are instantiated)

**Implementation:**

Locate the code in the agent that instantiates adapters. Add L2TP adapter:

```go
import (
    "github.com/amyrm/antimage/internal/node/adapter/l2tp"
    // ... other imports
)

// In adapter initialization:
l2tpAdapter := l2tp.New("/etc", "/var/lib/antimage")
adapters = append(adapters, l2tpAdapter)
```

**Verification:**

```bash
go build ./cmd/antimage-node
# Verify it compiles
```

---

## Phase B — Config Generation

**Goal:** Implement config file rendering for strongSwan, xl2tpd, PPP secrets, with ownership markers and checksums.

### Task B1: Create config rendering module

**Files:**
- Create: `internal/node/adapter/l2tp/config.go`
- Create: `internal/node/adapter/l2tp/config_test.go`

**Implementation:**

```go
// internal/node/adapter/l2tp/config.go
package l2tp

import (
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "strings"
    
    "github.com/amyrm/antimage/internal/node/adapter"
)

const (
    markerPrefix = "# antimage-managed:"
    
    ipsecConfPath    = "/etc/strongswan/ipsec.conf"
    xl2tpdConfPath   = "/etc/xl2tpd/xl2tpd.conf"
    chapSecretsPath  = "/etc/ppp/chap-secrets"
    pppOptionsPath   = "/etc/ppp/options.xl2tpd"
)

type ServiceParams struct {
    IPRange    string   `json:"ip_range"`
    LocalIP    string   `json:"local_ip"`
    PSK        string   `json:"psk"`
    DNSServers []string `json:"dns_servers"`
}

func parseServiceParams(raw json.RawMessage) (ServiceParams, error) {
    var p ServiceParams
    if err := json.Unmarshal(raw, &p); err != nil {
        return p, fmt.Errorf("parse service params: %w", err)
    }
    return p, nil
}

// renderIPsecConf generates strongSwan ipsec.conf
func renderIPsecConf(serviceID int64, params ServiceParams) string {
    payload := fmt.Sprintf(`config setup
    charondebug="ike 1, knl 1, cfg 0"
    uniqueids=no

conn antimage-l2tp
    keyexchange=ikev2
    ike=aes256-sha256-modp2048!
    esp=aes256-sha256!
    left=%%any
    leftsubnet=0.0.0.0/0
    right=%%any
    rightsubnet=%s
    authby=secret
    type=transport
    auto=add
`, params.LocalIP+"/24")
    
    checksum := checksumOf(payload)
    return fmt.Sprintf("%s service_id=%d checksum=%s\n%s",
        markerPrefix, serviceID, checksum, payload)
}

// renderXL2TPDConf generates xl2tpd.conf
func renderXL2TPDConf(serviceID int64, params ServiceParams) string {
    payload := fmt.Sprintf(`[global]
port = 1701

[lns default]
ip range = %s
local ip = %s
require chap = yes
refuse pap = yes
require authentication = yes
name = antimage-l2tp
pppoptfile = /etc/ppp/options.xl2tpd
length bit = yes
`, params.IPRange, params.LocalIP)
    
    checksum := checksumOf(payload)
    return fmt.Sprintf("; %s service_id=%d checksum=%s\n%s",
        strings.TrimPrefix(markerPrefix, "#"), serviceID, checksum, payload)
}

// renderCHAPSecrets generates /etc/ppp/chap-secrets
func renderCHAPSecrets(serviceID int64, subjects []adapter.Subject) string {
    var lines []string
    for _, subj := range subjects {
        for _, cred := range subj.Credentials {
            if cred.Kind == string(adapter.CredPassword) {
                // username * password *
                username := sanitizeUsername(subj.ID)
                lines = append(lines, fmt.Sprintf("%s\t*\t%s\t*", username, cred.Value))
            }
        }
    }
    
    payload := strings.Join(lines, "\n") + "\n"
    checksum := checksumOf(payload)
    return fmt.Sprintf("%s service_id=%d checksum=%s\n%s",
        markerPrefix, serviceID, checksum, payload)
}

// renderPPPOptions generates /etc/ppp/options.xl2tpd
func renderPPPOptions(serviceID int64, params ServiceParams) string {
    dnsLines := ""
    for _, dns := range params.DNSServers {
        dnsLines += fmt.Sprintf("ms-dns %s\n", dns)
    }
    
    payload := fmt.Sprintf(`require-mschap-v2
%snomppe
nodefaultroute
proxyarp
lcp-echo-interval 30
lcp-echo-failure 4
`, dnsLines)
    
    checksum := checksumOf(payload)
    return fmt.Sprintf("%s service_id=%d checksum=%s\n%s",
        markerPrefix, serviceID, checksum, payload)
}

func checksumOf(payload string) string {
    sum := sha256.Sum256([]byte(payload))
    return hex.EncodeToString(sum[:])
}

// sanitizeUsername converts subject ID to a PPP-safe username
func sanitizeUsername(subjectID int64) string {
    return fmt.Sprintf("user%d", subjectID)
}

// parseMarker extracts service_id and checksum from a marker line
func parseMarker(line string) (serviceID int64, checksum string, ok bool) {
    // TODO: implement parsing
    return 0, "", false
}
```

**Test:**

```go
// internal/node/adapter/l2tp/config_test.go
package l2tp

import (
    "encoding/json"
    "strings"
    "testing"
    
    "github.com/amyrm/antimage/internal/node/adapter"
)

func TestRenderIPsecConf(t *testing.T) {
    params := ServiceParams{
        IPRange:  "10.8.0.2-10.8.0.254",
        LocalIP:  "10.8.0.1",
        PSK:      "test-psk-secret",
        DNSServers: []string{"8.8.8.8"},
    }
    
    result := renderIPsecConf(123, params)
    
    if !strings.HasPrefix(result, markerPrefix) {
        t.Error("missing marker prefix")
    }
    if !strings.Contains(result, "service_id=123") {
        t.Error("missing service_id in marker")
    }
    if !strings.Contains(result, "checksum=") {
        t.Error("missing checksum in marker")
    }
    if !strings.Contains(result, "conn antimage-l2tp") {
        t.Error("missing connection config")
    }
}

func TestRenderCHAPSecrets(t *testing.T) {
    subjects := []adapter.Subject{
        {
            ID: 1,
            Credentials: []adapter.Credential{
                {Kind: "password", Value: "secret123"},
            },
        },
        {
            ID: 2,
            Credentials: []adapter.Credential{
                {Kind: "password", Value: "secret456"},
            },
        },
    }
    
    result := renderCHAPSecrets(123, subjects)
    
    if !strings.Contains(result, "user1") {
        t.Error("missing user1")
    }
    if !strings.Contains(result, "secret123") {
        t.Error("missing password for user1")
    }
    if !strings.Contains(result, "user2") {
        t.Error("missing user2")
    }
}

func TestServiceParamsValidation(t *testing.T) {
    raw := json.RawMessage(`{
        "ip_range": "10.8.0.2-10.8.0.254",
        "local_ip": "10.8.0.1",
        "psk": "my-secret-psk-key"
    }`)
    
    params, err := parseServiceParams(raw)
    if err != nil {
        t.Fatalf("parse failed: %v", err)
    }
    
    if params.IPRange != "10.8.0.2-10.8.0.254" {
        t.Errorf("wrong ip_range: %s", params.IPRange)
    }
}
```

**Verification:**

```bash
go test ./internal/node/adapter/l2tp/... -v
```

---

## Phase C — Observe/Plan/Apply/Probe Implementation

**Goal:** Implement reconciliation logic: read current state, diff against desired, apply changes, check health.

### Task C1: Implement Observe

**Files:**
- Modify: `internal/node/adapter/l2tp/adapter.go`
- Create: `internal/node/adapter/l2tp/observe.go`

**Implementation:**

```go
// internal/node/adapter/l2tp/observe.go
package l2tp

import (
    "context"
    "os"
    "strings"
    
    "github.com/amyrm/antimage/internal/node/adapter"
)

func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
    // Check if config files exist and are managed
    var services []adapter.ObservedService
    
    // For L2TP, we track one logical service across multiple files
    // Use service_id from any managed file we find
    
    ipsecManaged, ipsecChecksum := a.checkFile(ipsecConfPath)
    xl2tpManaged, xl2tpChecksum := a.checkFile(xl2tpdConfPath)
    chapManaged, chapChecksum := a.checkFile(chapSecretsPath)
    
    // If any file is managed, service is present
    present := ipsecManaged || xl2tpManaged || chapManaged
    managed := ipsecManaged && xl2tpManaged && chapManaged
    
    // Combine checksums (for simplicity; could track separately)
    combinedChecksum := ipsecChecksum + xl2tpChecksum + chapChecksum
    
    if present {
        services = append(services, adapter.ObservedService{
            ID:       0, // Set during Plan when we know service ID
            Present:  true,
            Managed:  managed,
            Checksum: combinedChecksum,
        })
    }
    
    return adapter.Observed{Services: services}, nil
}

func (a *Adapter) checkFile(path string) (managed bool, checksum string) {
    body, err := os.ReadFile(path)
    if err != nil {
        return false, ""
    }
    
    content := string(body)
    if !strings.HasPrefix(content, markerPrefix) && !strings.HasPrefix(content, "; antimage-managed:") {
        return false, ""
    }
    
    // Extract checksum from marker line
    lines := strings.Split(content, "\n")
    if len(lines) > 0 {
        // Parse marker: "# antimage-managed: service_id=X checksum=Y"
        marker := lines[0]
        parts := strings.Fields(marker)
        for _, part := range parts {
            if strings.HasPrefix(part, "checksum=") {
                checksum = strings.TrimPrefix(part, "checksum=")
                break
            }
        }
    }
    
    return true, checksum
}
```

---

### Task C2: Implement Plan

**Files:**
- Create: `internal/node/adapter/l2tp/plan.go`

**Implementation:**

```go
// internal/node/adapter/l2tp/plan.go
package l2tp

import (
    "context"
    "encoding/json"
    "fmt"
    
    "github.com/amyrm/antimage/internal/node/adapter"
)

const (
    StepInstallConfigs     = "install_configs"
    StepUpdateConfigs      = "update_configs"
    StepReloadCredentials  = "reload_credentials"
    StepRemoveConfigs      = "remove_configs"
)

func (a *Adapter) Plan(ctx context.Context, desired adapter.Desired, observed adapter.Observed) (adapter.Plan, error) {
    var steps []adapter.Step
    
    // Find L2TP service in desired state
    var l2tpService *adapter.Service
    for i := range desired.Services {
        if desired.Services[i].Kind == string(Kind) && desired.Services[i].Enabled {
            if l2tpService != nil {
                return adapter.Plan{}, fmt.Errorf("multiple L2TP services not supported")
            }
            l2tpService = &desired.Services[i]
        }
    }
    
    // Find observed L2TP service
    var observedService *adapter.ObservedService
    if len(observed.Services) > 0 {
        observedService = &observed.Services[0]
    }
    
    // Case 1: Service desired but not present → install
    if l2tpService != nil && observedService == nil {
        steps = append(steps, adapter.Step{
            Seq:        1,
            Kind:       StepInstallConfigs,
            Disruption: adapter.DisruptRestart,
            ServiceID:  l2tpService.ID,
            Payload:    l2tpService.Params,
        })
        return adapter.Plan{Steps: steps}, nil
    }
    
    // Case 2: Service not desired but present → remove
    if l2tpService == nil && observedService != nil && observedService.Present {
        steps = append(steps, adapter.Step{
            Seq:        1,
            Kind:       StepRemoveConfigs,
            Disruption: adapter.DisruptRestart,
            ServiceID:  0,
        })
        return adapter.Plan{Steps: steps}, nil
    }
    
    // Case 3: Service present and desired → check for changes
    if l2tpService != nil && observedService != nil && observedService.Managed {
        // Render current config and compare checksums
        params, err := parseServiceParams(l2tpService.Params)
        if err != nil {
            return adapter.Plan{}, err
        }
        
        desiredIPsec := renderIPsecConf(l2tpService.ID, params)
        desiredXL2TPD := renderXL2TPDConf(l2tpService.ID, params)
        desiredCHAP := renderCHAPSecrets(l2tpService.ID, desired.Subjects)
        
        desiredChecksum := checksumOf(desiredIPsec + desiredXL2TPD + desiredCHAP)
        
        if desiredChecksum != observedService.Checksum {
            // Determine if only users changed (reload) or params changed (restart)
            usersOnlyChanged := a.detectUsersOnlyChange(l2tpService, desired.Subjects, observedService)
            
            if usersOnlyChanged {
                steps = append(steps, adapter.Step{
                    Seq:        1,
                    Kind:       StepReloadCredentials,
                    Disruption: adapter.DisruptReload,
                    ServiceID:  l2tpService.ID,
                    Payload:    mustMarshal(desired.Subjects),
                })
            } else {
                steps = append(steps, adapter.Step{
                    Seq:        1,
                    Kind:       StepUpdateConfigs,
                    Disruption: adapter.DisruptRestart,
                    ServiceID:  l2tpService.ID,
                    Payload:    l2tpService.Params,
                })
            }
        }
    }
    
    return adapter.Plan{Steps: steps}, nil
}

func (a *Adapter) detectUsersOnlyChange(svc *adapter.Service, subjects []adapter.Subject, obs *adapter.ObservedService) bool {
    // TODO: Implement proper detection
    // For now, assume params change = restart
    return false
}

func mustMarshal(v interface{}) json.RawMessage {
    b, _ := json.Marshal(v)
    return b
}
```

---

### Task C3: Implement Apply

**Files:**
- Create: `internal/node/adapter/l2tp/apply.go`
- Create: `internal/node/adapter/l2tp/service.go` (systemctl wrappers)

**Implementation:**

```go
// internal/node/adapter/l2tp/apply.go
package l2tp

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "time"
    
    "github.com/amyrm/antimage/internal/node/adapter"
)

func (a *Adapter) Apply(ctx context.Context, step adapter.Step) (adapter.StepResult, error) {
    start := time.Now()
    
    var err error
    switch step.Kind {
    case StepInstallConfigs:
        err = a.applyInstallConfigs(ctx, step)
    case StepUpdateConfigs:
        err = a.applyUpdateConfigs(ctx, step)
    case StepReloadCredentials:
        err = a.applyReloadCredentials(ctx, step)
    case StepRemoveConfigs:
        err = a.applyRemoveConfigs(ctx, step)
    default:
        err = fmt.Errorf("unknown step kind: %s", step.Kind)
    }
    
    return adapter.StepResult{
        Seq:        step.Seq,
        Kind:       step.Kind,
        Disruption: step.Disruption,
        OK:         err == nil,
        Err:        errString(err),
        Duration:   time.Since(start),
    }, nil
}

func (a *Adapter) applyInstallConfigs(ctx context.Context, step adapter.Step) error {
    params, err := parseServiceParams(step.Payload)
    if err != nil {
        return err
    }
    
    // Write all config files
    if err := a.writeFile(ipsecConfPath, renderIPsecConf(step.ServiceID, params)); err != nil {
        return err
    }
    if err := a.writeFile(xl2tpdConfPath, renderXL2TPDConf(step.ServiceID, params)); err != nil {
        return err
    }
    if err := a.writeFile(pppOptionsPath, renderPPPOptions(step.ServiceID, params)); err != nil {
        return err
    }
    // CHAP secrets written separately (needs subjects)
    
    // Start services
    if err := startService("strongswan"); err != nil {
        return fmt.Errorf("start strongswan: %w", err)
    }
    if err := startService("xl2tpd"); err != nil {
        return fmt.Errorf("start xl2tpd: %w", err)
    }
    
    return nil
}

func (a *Adapter) applyUpdateConfigs(ctx context.Context, step adapter.Step) error {
    // Similar to install, but restart instead of start
    if err := a.applyInstallConfigs(ctx, step); err != nil {
        return err
    }
    
    if err := restartService("strongswan"); err != nil {
        return fmt.Errorf("restart strongswan: %w", err)
    }
    if err := restartService("xl2tpd"); err != nil {
        return fmt.Errorf("restart xl2tpd: %w", err)
    }
    
    return nil
}

func (a *Adapter) applyReloadCredentials(ctx context.Context, step adapter.Step) error {
    var subjects []adapter.Subject
    if err := json.Unmarshal(step.Payload, &subjects); err != nil {
        return fmt.Errorf("unmarshal subjects: %w", err)
    }
    
    // Write CHAP secrets
    content := renderCHAPSecrets(step.ServiceID, subjects)
    if err := a.writeFile(chapSecretsPath, content); err != nil {
        return err
    }
    
    // Reload strongSwan credentials
    if err := reloadStrongSwanCreds(); err != nil {
        return fmt.Errorf("reload strongswan creds: %w", err)
    }
    
    // SIGHUP xl2tpd
    if err := reloadService("xl2tpd"); err != nil {
        return fmt.Errorf("reload xl2tpd: %w", err)
    }
    
    return nil
}

func (a *Adapter) applyRemoveConfigs(ctx context.Context, step adapter.Step) error {
    // Stop services
    _ = stopService("strongswan") // best effort
    _ = stopService("xl2tpd")
    
    // Remove managed files
    _ = os.Remove(ipsecConfPath)
    _ = os.Remove(xl2tpdConfPath)
    _ = os.Remove(chapSecretsPath)
    _ = os.Remove(pppOptionsPath)
    
    return nil
}

func (a *Adapter) writeFile(path, content string) error {
    dir := filepath.Dir(path)
    if err := os.MkdirAll(dir, 0755); err != nil {
        return fmt.Errorf("mkdir %s: %w", dir, err)
    }
    
    // Atomic write: temp + rename
    tmp := path + ".tmp"
    if err := os.WriteFile(tmp, []byte(content), 0600); err != nil {
        return fmt.Errorf("write %s: %w", tmp, err)
    }
    if err := os.Rename(tmp, path); err != nil {
        os.Remove(tmp)
        return fmt.Errorf("rename %s: %w", path, err)
    }
    
    return nil
}

func errString(err error) string {
    if err == nil {
        return ""
    }
    return err.Error()
}
```

```go
// internal/node/adapter/l2tp/service.go
package l2tp

import (
    "fmt"
    "os/exec"
)

func startService(name string) error {
    return exec.Command("systemctl", "start", name).Run()
}

func stopService(name string) error {
    return exec.Command("systemctl", "stop", name).Run()
}

func restartService(name string) error {
    return exec.Command("systemctl", "restart", name).Run()
}

func reloadService(name string) error {
    return exec.Command("systemctl", "reload", name).Run()
}

func isServiceActive(name string) bool {
    err := exec.Command("systemctl", "is-active", "--quiet", name).Run()
    return err == nil
}

func reloadStrongSwanCreds() error {
    return exec.Command("swanctl", "--load-creds").Run()
}
```

---

### Task C4: Implement Probe

**Files:**
- Modify: `internal/node/adapter/l2tp/adapter.go`

**Implementation:**

```go
func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
    // Check if services are running
    if !isServiceActive("strongswan") {
        return adapter.Health{
            OK:     false,
            Detail: "strongswan service not running",
        }, nil
    }
    
    if !isServiceActive("xl2tpd") {
        return adapter.Health{
            OK:     false,
            Detail: "xl2tpd service not running",
        }, nil
    }
    
    // TODO: Check listening ports (UDP 500, 4500, 1701)
    
    return adapter.Health{OK: true, Detail: "all services running"}, nil
}
```

**Verification:**

```bash
go test ./internal/node/adapter/l2tp/... -v
```

---

## Phase D — nftables Accounting and UsageReporter

**Goal:** Implement per-IP traffic accounting using nftables, persist cursor state, and report deltas via UsageReporter interface.

### Task D1: Implement nftables setup and counter reading

**Files:**
- Create: `internal/node/adapter/l2tp/accounting.go`
- Create: `internal/node/adapter/l2tp/accounting_test.go`

**Implementation:**

```go
// internal/node/adapter/l2tp/accounting.go
package l2tp

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "strconv"
    "strings"
    "time"
    
    "github.com/amyrm/antimage/internal/node/adapter"
)

type accountingCursor struct {
    LastPoll int64                    `json:"last_poll"`
    Counters map[string]trafficCounter `json:"counters"`
}

type trafficCounter struct {
    RxBytes uint64 `json:"rx"`
    TxBytes uint64 `json:"tx"`
}

func (a *Adapter) cursorPath() string {
    return filepath.Join(a.stateDir, "l2tp-accounting.json")
}

func (a *Adapter) Usage(ctx context.Context) ([]adapter.UsageSample, error) {
    // 1. Read previous cursor
    prev, err := a.loadCursor()
    if err != nil {
        // First run or corrupted cursor → start fresh
        prev = accountingCursor{
            LastPoll: time.Now().Unix(),
            Counters: make(map[string]trafficCounter),
        }
    }
    
    // 2. Read current nftables counters
    current, err := a.readNftablesCounters()
    if err != nil {
        return nil, fmt.Errorf("read nftables: %w", err)
    }
    
    // 3. Compute deltas
    var samples []adapter.UsageSample
    for ip, curCount := range current {
        prevCount := prev.Counters[ip]
        
        // Detect counter resets (service restart)
        if curCount.RxBytes < prevCount.RxBytes || curCount.TxBytes < prevCount.TxBytes {
            // Reset detected, start from current
            prevCount = trafficCounter{}
        }
        
        deltaRx := curCount.RxBytes - prevCount.RxBytes
        deltaTx := curCount.TxBytes - prevCount.TxBytes
        
        if deltaRx > 0 || deltaTx > 0 {
            // Map IP to subject ID
            subjectID, err := a.ipToSubjectID(ip)
            if err != nil {
                // IP not mapped (stale session), skip
                continue
            }
            
            samples = append(samples, adapter.UsageSample{
                SubjectID:     subjectID,
                UplinkBytes:   deltaTx,
                DownlinkBytes: deltaRx,
            })
        }
    }
    
    // 4. Save new cursor
    newCursor := accountingCursor{
        LastPoll: time.Now().Unix(),
        Counters: current,
    }
    if err := a.saveCursor(newCursor); err != nil {
        return nil, fmt.Errorf("save cursor: %w", err)
    }
    
    return samples, nil
}

func (a *Adapter) loadCursor() (accountingCursor, error) {
    var cursor accountingCursor
    data, err := os.ReadFile(a.cursorPath())
    if err != nil {
        return cursor, err
    }
    if err := json.Unmarshal(data, &cursor); err != nil {
        return cursor, err
    }
    return cursor, nil
}

func (a *Adapter) saveCursor(cursor accountingCursor) error {
    data, err := json.Marshal(cursor)
    if err != nil {
        return err
    }
    
    dir := filepath.Dir(a.cursorPath())
    if err := os.MkdirAll(dir, 0755); err != nil {
        return err
    }
    
    tmp := a.cursorPath() + ".tmp"
    if err := os.WriteFile(tmp, data, 0600); err != nil {
        return err
    }
    return os.Rename(tmp, a.cursorPath())
}

func (a *Adapter) readNftablesCounters() (map[string]trafficCounter, error) {
    // Run: nft -j list table inet antimage_l2tp
    cmd := exec.Command("nft", "-j", "list", "table", "inet", "antimage_l2tp")
    output, err := cmd.CombinedOutput()
    if err != nil {
        // Table doesn't exist yet
        return make(map[string]trafficCounter), nil
    }
    
    // Parse JSON output
    // TODO: Implement proper nftables JSON parsing
    // For now, return empty (Phase E will complete this)
    
    return make(map[string]trafficCounter), nil
}

func (a *Adapter) ipToSubjectID(ip string) (int64, error) {
    // Map IP to subject ID
    // TODO: Read from /var/lib/antimage/l2tp-sessions.json
    // For now, parse from username mapping
    
    return 0, fmt.Errorf("not implemented")
}
```

**Test:**

```go
// internal/node/adapter/l2tp/accounting_test.go
package l2tp

import (
    "testing"
)

func TestAccountingCursorPersistence(t *testing.T) {
    a := New("/tmp/l2tp-test", "/tmp/l2tp-state-test")
    
    cursor := accountingCursor{
        LastPoll: 1692547200,
        Counters: map[string]trafficCounter{
            "10.8.0.2": {RxBytes: 1048576, TxBytes: 2097152},
        },
    }
    
    if err := a.saveCursor(cursor); err != nil {
        t.Fatalf("save cursor: %v", err)
    }
    
    loaded, err := a.loadCursor()
    if err != nil {
        t.Fatalf("load cursor: %v", err)
    }
    
    if loaded.LastPoll != cursor.LastPoll {
        t.Errorf("want LastPoll %d, got %d", cursor.LastPoll, loaded.LastPoll)
    }
    
    if loaded.Counters["10.8.0.2"].RxBytes != 1048576 {
        t.Error("counter mismatch")
    }
}
```

---

## Phase E — End-to-End Verification and Cleanup

**Goal:** Complete implementation, run all tests, verify integration with SP1-SP5.

### Task E1: Complete nftables integration

**Files:**
- Complete: `internal/node/adapter/l2tp/accounting.go`

**Implementation:**

- Implement full nftables JSON parsing
- Implement IP-to-subject-ID mapping (read from session logs or derive from username)
- Add nftables table setup in Apply steps
- Create PPP hook scripts for session tracking

### Task E2: Integration tests

**Files:**
- Create: `internal/node/adapter/l2tp/integration_test.go`

**Implementation:**

```go
func TestObservePlanApplyCycle(t *testing.T) {
    // Create temp directories
    // Build desired state
    // Run Observe → Plan → Apply
    // Verify configs written
    // Run Observe again → should converge (empty plan)
}

func TestDriftDetection(t *testing.T) {
    // Apply a config
    // Hand-edit a file
    // Run Observe → verify drift reported
}
```

### Task E3: Final verification

**Commands:**

```bash
# Run all tests
go test ./internal/node/adapter/l2tp/... -v -race

# Run full test suite
go test ./... -v

# Vet
go vet ./...

# Lint
golangci-lint run

# Verify no SP1-SP5 regressions
go test ./internal/panel/... -v
go test ./internal/node/... -v
```

### Task E4: Documentation and commit

**Files:**
- Update: `README.md` (if applicable)
- Create: Commit with message following SP6 completion

**Commit message:**

```
feat(sp6): implement L2TP/IPsec adapter

Implements the third protocol family adapter per the adapter contract:

- strongSwan (IPsec) configuration generation
- xl2tpd (L2TP) configuration generation
- PPP CHAP secrets management
- Per-IP nftables accounting via UsageReporter interface
- Service lifecycle: install, update, reload, remove
- Disruption levels: reload for users, restart for params
- Ownership markers and drift detection

Integrates with:
- SP1: adapter contract, reconciliation
- SP2: password credential kind
- SP3: accounting ingestion
- SP5: adapter registry, metrics

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
```

---

## Definition of Done Checklist

- [ ] Phase A complete: adapter skeleton compiles and registers
- [ ] Phase B complete: config rendering tests pass
- [ ] Phase C complete: Observe/Plan/Apply/Probe implemented
- [ ] Phase D complete: nftables accounting functional
- [ ] Phase E complete: all tests pass
- [ ] `go test ./...` passes with 0 failures
- [ ] `go vet ./...` reports 0 issues
- [ ] `golangci-lint run` reports 0 issues
- [ ] No SP1-SP5 regressions detected
- [ ] L2TP adapter registered in agent
- [ ] Service schema valid JSON
- [ ] Ownership markers present in all config files
- [ ] Atomic writes (temp + rename) used everywhere
- [ ] Disruption levels correct (reload vs restart)
- [ ] UsageReporter interface implemented
- [ ] Accounting cursor persists across restarts
- [ ] Integration test covers full reconciliation cycle
- [ ] All files committed with proper commit message

---

## Notes

- **Root privileges required:** L2TP adapter must run as root (or with CAP_NET_ADMIN + CAP_DAC_OVERRIDE)
- **Dependencies:** strongswan, xl2tpd, ppp, nftables packages must be installed on node
- **Single service limit:** Adapter enforces one L2TP service per node
- **Session tracking:** PPP hooks (ip-up/ip-down) are optional enhancement; initial implementation can poll xl2tpd status
- **Testing complexity:** Full E2E test with real strongSwan requires container environment; integration tests suffice for SP6 completion
