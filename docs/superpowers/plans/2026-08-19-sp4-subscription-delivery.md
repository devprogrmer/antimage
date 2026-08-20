# antimage SP4 — Subscription Delivery Implementation Plan

**Goal:** Implement subscription token issuance, revocation, multi-format config rendering (v2ray/Clash/sing-box), UA detection, and rate limiting.

**Spec:** `docs/superpowers/specs/2026-08-19-sp4-subscription-delivery.md`. Read it before Task 1.

**Prerequisites:** SP1, SP2, SP3 complete and passing all tests.

---

## Task Overview

| # | Task | Est. | Files |
|---|------|------|-------|
| 1 | Database migration: subscription_token column | 15m | `00012_subscriptions.sql` |
| 2 | Token generation and management | 30m | `subjects/tokens.go`, `tokens_test.go` |
| 3 | Rate limiter implementation | 45m | `subscriptions/ratelimit.go`, `ratelimit_test.go` |
| 4 | Format detection (UA parsing) | 20m | `subscriptions/format.go`, `format_test.go` |
| 5 | v2ray renderer | 1h | `subscriptions/v2ray.go`, `v2ray_test.go` |
| 6 | Clash renderer | 1h | `subscriptions/clash.go`, `clash_test.go` |
| 7 | sing-box renderer | 1h | `subscriptions/singbox.go`, `singbox_test.go` |
| 8 | Subscription HTTP endpoint | 1h | `httpapi/subscribe.go`, `subscribe_test.go` |
| 9 | Admin API extensions (token revocation) | 30m | `httpapi/subjects.go` (extend) |
| 10 | Integration tests | 1h | `subscriptions/integration_test.go` |
| 11 | E2E tests | 45m | `test/e2e/sp4_test.go` |
| 12 | Documentation and cleanup | 15m | README updates |

**Total:** ~8 hours

---

## Task 1: Database Migration

**Files:**
- Create: `internal/panel/store/migrations/00012_subscriptions.sql`

**Schema changes:**
```sql
-- Add subscription_token column to subjects
ALTER TABLE subjects ADD COLUMN subscription_token TEXT NOT NULL DEFAULT '';

-- Index for fast token lookup (sparse: only non-empty tokens)
CREATE UNIQUE INDEX idx_subjects_subscription_token 
  ON subjects(subscription_token) 
  WHERE subscription_token != '';
```

**Test:**
- Apply migration on fresh DB
- Apply on DB with existing subjects (tokens start empty)
- Verify index exists and is unique

**Interfaces:**
- Consumes: `subjects` table from SP1
- Produces: `subjects.subscription_token` column
- Used by: Task 2 (token management)

---

## Task 2: Token Generation and Management

**Files:**
- Create: `internal/panel/subjects/tokens.go`
- Create: `internal/panel/subjects/tokens_test.go`

**API:**
```go
// GenerateToken creates a cryptographically random subscription token.
// Returns base64url-encoded 32 bytes (~43 characters).
func GenerateToken() (string, error)

// EnsureToken returns the subject's subscription token, generating one if empty.
// Lazy initialization: existing subjects get tokens on first access.
func EnsureToken(ctx context.Context, st store.Store, subjectID int64) (string, error)

// RevokeToken regenerates a subject's subscription token, invalidating the old one.
// Returns the new token.
func RevokeToken(ctx context.Context, st store.Store, subjectID int64) (string, error)

// LookupByToken finds a subject by subscription token.
// Returns (subjectID, nil) or (0, ErrNotFound).
func LookupByToken(ctx context.Context, st store.Store, token string) (int64, error)
```

**Implementation:**
- `GenerateToken`: 32 bytes from `crypto/rand`, `base64.RawURLEncoding`
- `EnsureToken`: SELECT token, if empty then generate+UPDATE
- `RevokeToken`: generate new token, UPDATE, return new token
- `LookupByToken`: SELECT id WHERE token = ? AND enabled = 1

**Tests:**
- Token entropy (check length, uniqueness, URL-safe characters)
- EnsureToken idempotent (call twice, get same token)
- RevokeToken invalidates old token
- LookupByToken returns 0 for disabled subjects

**Interfaces:**
- Consumes: `subjects` table, `store.Store`
- Produces: Token strings
- Used by: Task 8 (subscription endpoint), Task 9 (admin API)

---

## Task 3: Rate Limiter

**Files:**
- Create: `internal/panel/subscriptions/ratelimit.go`
- Create: `internal/panel/subscriptions/ratelimit_test.go`

**API:**
```go
// RateLimiter enforces per-token request limits.
type RateLimiter interface {
	// Allow returns true if the request is allowed, false if rate limit exceeded.
	Allow(token string) bool
}

// NewSlidingWindowLimiter creates a rate limiter with the given limit and window.
// Example: NewSlidingWindowLimiter(10, time.Minute) = 10 requests per minute.
func NewSlidingWindowLimiter(limit int, window time.Duration) RateLimiter
```

**Implementation:**
- In-memory map: `token → []timestamp`
- Sliding window: keep only timestamps within `now - window`
- Prune old entries periodically (background goroutine)
- Thread-safe (use `sync.RWMutex`)

**Tests:**
- 10 requests in 1 minute → all allowed
- 11th request → denied
- Wait 61 seconds → allowed again (window slides)
- Concurrent access (multiple tokens, multiple goroutines)
- Memory cleanup (old tokens pruned after inactivity)

**Interfaces:**
- Consumes: Nothing (standalone)
- Produces: Allow/deny decisions
- Used by: Task 8 (subscription endpoint)

---

## Task 4: Format Detection

**Files:**
- Create: `internal/panel/subscriptions/format.go`
- Create: `internal/panel/subscriptions/format_test.go`

**API:**
```go
// Format represents a subscription config format.
type Format string

const (
	FormatV2Ray   Format = "v2ray"
	FormatClash   Format = "clash"
	FormatSingBox Format = "singbox"
)

// DetectFormat parses the User-Agent header and returns the appropriate format.
// Defaults to FormatV2Ray for maximum compatibility.
func DetectFormat(userAgent string) Format
```

**Detection logic:**
- Contains `Clash` (case-insensitive) → `FormatClash`
- Contains `sing-box`, `SFI`, `SFA` → `FormatSingBox`
- Contains `v2rayN`, `v2rayNG` → `FormatV2Ray`
- Default → `FormatV2Ray`

**Tests:**
- UA = "Clash/1.0" → FormatClash
- UA = "v2rayNG/1.8.0" → FormatV2Ray
- UA = "sing-box/1.3.0" → FormatSingBox
- UA = "curl/7.68.0" → FormatV2Ray (default)
- UA = "" → FormatV2Ray (default)

**Interfaces:**
- Consumes: HTTP User-Agent string
- Produces: Format enum
- Used by: Task 8 (subscription endpoint)

---

## Task 5: v2ray Renderer

**Files:**
- Create: `internal/panel/subscriptions/v2ray.go`
- Create: `internal/panel/subscriptions/v2ray_test.go`

**API:**
```go
// V2RayRenderer renders v2ray-format subscriptions (base64-encoded vmess:// lines).
type V2RayRenderer struct{}

// Render returns base64-encoded subscription content and content-type.
func (r *V2RayRenderer) Render(ctx context.Context, servers []Server) ([]byte, string, error)

// Server represents one node+credential pair to render.
type Server struct {
	NodeID       int64
	NodeName     string
	NodeAddress  string
	InboundPort  int
	Protocol     string // "vless", "vmess", "trojan"
	CredentialID string // UUID for vless/vmess, password for trojan
	TLSEnabled   bool
	// Add fields as needed for SNI, ALPN, etc.
}
```

**Implementation:**
- For each server, generate a `vmess://base64(json)` or `vless://...` URI
- Join with newlines
- Base64-encode the entire result
- Return with `Content-Type: text/plain; charset=utf-8`

**v2ray URI formats:**
- VLESS: `vless://uuid@host:port?type=tcp&security=tls#name`
- VMess: `vmess://base64({"v":"2","ps":"name","add":"host","port":443,...})`
- Trojan: `trojan://password@host:port?security=tls#name`

**Tests:**
- Single server → valid base64 output
- Multiple servers → newline-separated
- Decode and verify JSON structure for VMess
- Protocol-specific formatting (vless vs vmess vs trojan)

**Interfaces:**
- Consumes: `[]Server` (aggregated from DB)
- Produces: Base64-encoded subscription config
- Used by: Task 8 (subscription endpoint)

---

## Task 6: Clash Renderer

**Files:**
- Create: `internal/panel/subscriptions/clash.go`
- Create: `internal/panel/subscriptions/clash_test.go`

**API:**
```go
// ClashRenderer renders Clash-format YAML subscriptions.
type ClashRenderer struct{}

func (r *ClashRenderer) Render(ctx context.Context, servers []Server) ([]byte, string, error)
```

**Implementation:**
- Generate YAML with `proxies:` array
- Each proxy has: `name`, `type`, `server`, `port`, `uuid`/`password`, `tls`, `skip-cert-verify`, etc.
- Return with `Content-Type: application/x-yaml`

**Clash YAML structure:**
```yaml
proxies:
  - name: "Node1-VLESS"
    type: vless
    server: node1.example.com
    port: 443
    uuid: <uuid>
    tls: true
    skip-cert-verify: false
```

**Tests:**
- Valid YAML output (parse with `gopkg.in/yaml.v3`)
- Correct proxy structure
- Protocol-specific fields (vless vs vmess vs trojan)

**Interfaces:**
- Consumes: `[]Server`
- Produces: YAML config
- Used by: Task 8 (subscription endpoint)

---

## Task 7: sing-box Renderer

**Files:**
- Create: `internal/panel/subscriptions/singbox.go`
- Create: `internal/panel/subscriptions/singbox_test.go`

**API:**
```go
// SingBoxRenderer renders sing-box-format JSON subscriptions.
type SingBoxRenderer struct{}

func (r *SingBoxRenderer) Render(ctx context.Context, servers []Server) ([]byte, string, error)
```

**Implementation:**
- Generate JSON with `outbounds:` array
- Each outbound has: `type`, `tag`, `server`, `server_port`, `uuid`/`password`, `tls`, etc.
- Return with `Content-Type: application/json`

**sing-box JSON structure:**
```json
{
  "outbounds": [
    {
      "type": "vless",
      "tag": "Node1-VLESS",
      "server": "node1.example.com",
      "server_port": 443,
      "uuid": "<uuid>",
      "tls": {
        "enabled": true,
        "insecure": false
      }
    }
  ]
}
```

**Tests:**
- Valid JSON output
- Correct outbound structure
- Protocol-specific fields

**Interfaces:**
- Consumes: `[]Server`
- Produces: JSON config
- Used by: Task 8 (subscription endpoint)

---

## Task 8: Subscription HTTP Endpoint

**Files:**
- Create: `internal/panel/httpapi/subscribe.go`
- Create: `internal/panel/httpapi/subscribe_test.go`
- Modify: `internal/panel/httpapi/router.go` (add route)

**API:**
```go
// GET /api/v1/subscribe/{token}
func (s *Service) handleSubscribe(w http.ResponseWriter, r *http.Request)
```

**Implementation flow:**
1. Extract `{token}` from URL path
2. Check rate limit → 429 if exceeded
3. Lookup subject by token → 404 if not found
4. Check subject enabled, not expired, not frozen → 404 if not eligible
5. Query all nodes serving this subject (JOIN nodes, services, inbounds, subject_credentials)
6. If no nodes → 503 Service Unavailable
7. Unseal credentials for each node (use `secrets.Unsealer`)
8. Build `[]Server` list
9. Detect format from `User-Agent` header
10. Select renderer (v2ray/Clash/sing-box)
11. Render config
12. Return with appropriate `Content-Type`
13. Audit log: subject_id, remote IP, user-agent (token redacted)

**HTTP responses:**
- `200 OK` + config body
- `404 Not Found` — invalid token, disabled/expired/frozen subject
- `429 Too Many Requests` + `Retry-After: 60`
- `503 Service Unavailable` — subject has no nodes
- `500 Internal Server Error` — rendering failure

**Tests:**
- Valid token → 200 + rendered config
- Invalid token → 404
- Disabled subject → 404
- Expired subject → 404
- Frozen subject → 404
- No nodes → 503
- Rate limit exceeded → 429
- Different User-Agents → different formats

**Interfaces:**
- Consumes: All previous tasks (tokens, rate limiter, renderers, format detection)
- Produces: HTTP subscription endpoint
- Used by: End-user VPN clients

---

## Task 9: Admin API Extensions

**Files:**
- Modify: `internal/panel/httpapi/subjects.go`
- Modify: `internal/panel/httpapi/subjects_test.go`

**Changes:**

**1. Add `subscription_url` to subject JSON responses:**
```go
type SubjectJSON struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	SubscriptionURL string `json:"subscription_url"`
	// ... existing fields
}
```

Construct URL: `https://{panel-host}/api/v1/subscribe/{token}`

**2. Add token revocation endpoint:**
```
POST /api/v1/subjects/{id}/revoke-token
```
- Requires `subjects:write` permission
- Calls `RevokeToken(ctx, st, subjectID)`
- Returns new subscription URL
- Audited: "revoke subscription token for subject {id}"

**Tests:**
- GET /api/v1/subjects/{id} includes subscription_url
- POST /api/v1/subjects/{id}/revoke-token → new token, old token invalid
- Permission enforcement (403 without subjects:write)
- Audit log verification

**Interfaces:**
- Consumes: Task 2 (token management)
- Produces: Extended admin API
- Used by: Operators via UI/API

---

## Task 10: Integration Tests

**Files:**
- Create: `internal/panel/subscriptions/integration_test.go`

**Test scenarios:**

**1. Token lifecycle:**
- Create subject → ensure token → fetch subscription → revoke token → old URL returns 404

**2. Multi-node aggregation:**
- Create 3 nodes with inbounds
- Grant subject to all 3 nodes
- Fetch subscription → verify all 3 nodes appear in config

**3. Expiry enforcement:**
- Create subject with `expires_at` in past → 404
- Update expiry to future → 200

**4. Quota freeze enforcement:**
- Freeze subject for quota → 404
- Unfreeze → 200

**5. Rate limiting:**
- Make 10 requests → all succeed
- 11th request → 429
- Wait 61 seconds → succeeds again

**6. Format selection:**
- Fetch with `User-Agent: Clash` → YAML response
- Fetch with `User-Agent: v2rayNG` → base64 response
- Fetch with `User-Agent: sing-box` → JSON response

**Interfaces:**
- Consumes: All SP4 components
- Produces: Integration test coverage
- Used by: CI verification

---

## Task 11: E2E Tests

**Files:**
- Create: `test/e2e/sp4_test.go`

**Test scenarios:**

**1. Real HTTP subscription fetch:**
- Start panel + agent
- Enroll node, create subject, grant service
- Fetch `GET /api/v1/subscribe/{token}` via real HTTP client
- Verify response format and content

**2. Credential unsealing verification:**
- Render subscription
- Parse rendered UUID/password
- Verify it matches the sealed credential in DB (after unsealing)

**3. Multi-format rendering:**
- Same subscription token
- Fetch with different User-Agent headers
- Verify each format is valid (parse YAML/JSON, decode base64)

**Interfaces:**
- Consumes: SP1/SP2/SP3 infrastructure + SP4 subscription endpoint
- Produces: End-to-end test coverage
- Used by: CI acceptance criteria

---

## Task 12: Documentation and Cleanup

**Files:**
- Update: `README.md` (mention SP4 in status section)
- Review: All TODOs removed
- Review: All debug prints removed
- Review: No uncommitted test files

**Checklist:**
- [ ] README mentions SP4 subscription delivery
- [ ] No `TODO(sp4)` comments remain
- [ ] No debug `fmt.Println` in production code
- [ ] All tests pass locally
- [ ] All linters pass
- [ ] Git status clean (no workspace artifacts)

---

## Verification Commands

Before declaring SP4 complete:

```bash
# Build
go build ./...

# Unit tests
go test ./internal/panel/subjects/... -v
go test ./internal/panel/subscriptions/... -v
go test ./internal/panel/httpapi/... -run Subscribe -v

# Integration tests
go test ./internal/panel/subscriptions/... -run Integration -v

# E2E tests
go test ./test/e2e/... -tags e2e -run SP4 -v

# Full suite (SP1/SP2/SP3/SP4)
go test ./... -count=1 -timeout 15m

# Lint
golangci-lint run --timeout 5m

# CI gates
make check-imports
make check-rtl
go vet ./...
```

---

## Acceptance Criteria

SP4 is complete when:

- [ ] Task 1: Migration applied, index created
- [ ] Task 2: Token generation, ensure, revoke, lookup working
- [ ] Task 3: Rate limiter enforces 10 req/min
- [ ] Task 4: Format detection works for all UAs
- [ ] Task 5: v2ray renderer produces valid base64 output
- [ ] Task 6: Clash renderer produces valid YAML
- [ ] Task 7: sing-box renderer produces valid JSON
- [ ] Task 8: Subscription endpoint handles all cases (200/404/429/503)
- [ ] Task 9: Admin API includes subscription_url, revoke-token works
- [ ] Task 10: All integration tests pass
- [ ] Task 11: E2E tests pass
- [ ] Task 12: Documentation updated, no TODOs
- [ ] All SP1/SP2/SP3 tests still pass (no regressions)
- [ ] CI passes (go, realruntime, web checks)

---

## Implementation Notes

**Thread safety:**
- Rate limiter must be thread-safe (concurrent requests)
- Token generation uses `crypto/rand` (already thread-safe)

**Performance:**
- Subscription rendering is on-demand (no caching)
- Each request queries DB + unseals credentials (acceptable: authenticated users only, rate-limited)
- For high-traffic panels, consider caching rendered configs (future optimization, not SP4)

**Security:**
- Tokens never logged in plaintext (audit logs show subject_id)
- Credentials unsealed only during rendering (exist in memory briefly)
- Rate limiting prevents enumeration attacks

**Backward compatibility:**
- Existing subjects work (tokens generated on first access)
- No changes to agent/node (subscription is panel-only)
- No breaking changes to SP1/SP2/SP3 APIs

---

## Open Issues

None. Implementation proceeds as specified.

---

## Revision History

- **2026-08-19:** Initial SP4 implementation plan
