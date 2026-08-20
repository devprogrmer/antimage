# antimage SP4 — Subscription Delivery Design

**Date:** 2026-08-19  
**Status:** Approved  
**Scope:** Sub-project 4 of 8  
**Prerequisites:** SP1 (control-plane spine), SP2 (Xray/sing-box adapter), SP3 (accounting and quotas)

---

## 1. Scope and Goal

**SP4 delivers subscription endpoints** that render protocol-specific configuration for end-user VPN clients. A subject (user) is issued a stable, revocable subscription token. When a client fetches the subscription URL, the panel:

1. Validates the token
2. Detects the client's User-Agent to determine the config format
3. Gathers all nodes serving this subject (cross-node aggregation)
4. Renders the appropriate format (v2ray/Clash/sing-box for Xray/sing-box protocols; `.ovpn` profiles for OpenVPN)
5. Rate-limits requests to prevent abuse

**Out of scope for SP4:**
- OpenVPN and L2TP/IPsec protocol support (SP5, SP6)
- Reseller branding or multi-tenant subscription domains (SP8)
- Subscription update notifications or push mechanisms

---

## 2. Architecture

### 2.1 Token Model

Each subject has **one stable subscription token** that identifies them across all protocols and nodes. The token:

- Is a URL-safe random string (32 bytes base64url-encoded, ~43 characters)
- Is stored in the `subjects` table
- Can be revoked (token regenerated, old token invalidated immediately)
- Is never logged or audited in plaintext (appears as `<redacted>` in logs)
- Is the ONLY authentication mechanism for subscription endpoints (no session cookies, no bearer tokens)

**Design rationale:**
- One token per subject (not per protocol, not per node) simplifies UX: one QR code, one link
- Stable tokens allow bookmarking and auto-update in clients
- Revocation = regeneration (no separate revocation list, instant invalidation)
- URL parameter (not header) for maximum client compatibility

### 2.2 Subscription Endpoint

**URL pattern:**
```
GET /api/v1/subscribe/{token}
```

**No authentication required** — the token IS the authentication.

**Response:**
- `200 OK` with `Content-Type` based on detected format
- `404 Not Found` if token is invalid or subject is disabled/expired/frozen
- `429 Too Many Requests` if rate limit exceeded
- `503 Service Unavailable` if no nodes are currently serving this subject

**UA detection logic:**
- `User-Agent` contains `v2rayN` or `v2rayNG` → v2ray format (base64-encoded vmess:// URIs)
- `User-Agent` contains `Clash` → Clash YAML
- `User-Agent` contains `sing-box` or `SFI` or `SFA` → sing-box JSON
- Default fallback → v2ray format (widest compatibility)

**Rate limiting:**
- Per-token: 10 requests per minute (prevents abuse, allows legitimate retries)
- Implemented via in-memory sliding window (no persistence required)
- HTTP `429` response includes `Retry-After` header

### 2.3 Config Rendering

Each format aggregates **all nodes** currently serving the subject:

**v2ray format:**
```
base64(vmess://... + "\n" + vmess://... + "\n" + ...)
```
Each line is a base64-encoded JSON vmess:// URI.

**Clash format:**
```yaml
proxies:
  - name: "Node1-VLESS"
    type: vless
    server: node1.example.com
    port: 443
    uuid: <credential-uuid>
    ...
  - name: "Node2-VLESS"
    ...
```

**sing-box format:**
```json
{
  "outbounds": [
    {
      "type": "vless",
      "tag": "Node1-VLESS",
      "server": "node1.example.com",
      "server_port": 443,
      "uuid": "<credential-uuid>",
      ...
    },
    ...
  ]
}
```

**Node selection:** Only nodes where:
- Node is enabled (`nodes.enabled = 1`)
- Subject is enabled (`subjects.enabled = 1`)
- Subject is not expired (`subjects.expires_at IS NULL OR expires_at > now`)
- Subject is not frozen for quota (`subjects.frozen_at IS NULL`)
- A service exists linking the subject to the node (`services.enabled = 1`)
- The subject has a credential for one of the node's inbounds

**Credential exposure:** Subscription rendering unseals the subject's credentials on-demand. Plaintext exists only in memory during rendering, never logged.

---

## 3. Database Schema

### 3.1 subjects table (extend existing)

Add one column:

```sql
ALTER TABLE subjects ADD COLUMN subscription_token TEXT NOT NULL DEFAULT '';
```

**Migration strategy:**
- Existing subjects get empty tokens
- Token is generated on first access (lazy initialization) or via explicit admin action
- Index on `subscription_token` for fast lookup (unique, not null after initialization)

### 3.2 No additional tables

Revocation is implicit (regenerate token). Rate limiting is in-memory (no persistent storage).

---

## 4. API Changes

### 4.1 New Public Endpoint

**`GET /api/v1/subscribe/{token}`**

- **Public** (no session required)
- **Unauthenticated** (token is the credential)
- **Rate-limited** per token
- **Returns:** Rendered subscription config in detected format
- **Audit:** Access logged with subject_id, remote IP, User-Agent (token redacted)

### 4.2 Admin API Extensions

**Subject creation/detail responses** include `subscription_url`:
```json
{
  "id": 123,
  "name": "user@example.com",
  "subscription_url": "https://panel.example.com/api/v1/subscribe/abc123...",
  ...
}
```

**New admin action:**
```
POST /api/v1/subjects/{id}/revoke-token
```
Regenerates the subscription token, invalidating all previous URLs. Requires `subjects:write` permission. Audited.

---

## 5. Security Considerations

### 5.1 Token Entropy

32 random bytes → ~43 characters base64url → ~256 bits entropy. Brute-force is infeasible.

### 5.2 Token Leakage

Tokens appear in:
- Subscription URLs (shareable, but not logged by the panel)
- HTTP access logs (if enabled externally — operators must sanitize logs)
- Client-side bookmarks/history

**Not logged by antimage:**
- Audit trail records `subject_id`, not token
- Application logs redact tokens

### 5.3 Revocation

Instant: regenerate token in DB, old token immediately invalid (no caching, no grace period).

### 5.4 Rate Limiting

Prevents:
- Token enumeration attacks (10 req/min per token = 600/hour max, infeasible to brute-force 2^256 space)
- Accidental DoS from misconfigured clients

Does not prevent:
- Distributed attacks across many tokens (acceptable: each token represents a paid subject)

---

## 6. Implementation Components

### 6.1 Panel

**`internal/panel/subjects/tokens.go`** (new):
- `GenerateToken() string` — 32 random bytes, base64url
- `EnsureToken(ctx, subjectID) (string, error)` — lazy initialization
- `RevokeToken(ctx, subjectID) error` — regenerate and update DB

**`internal/panel/subscriptions/renderer.go`** (new):
- `type Renderer interface { Render(ctx, subject, nodes, credentials) ([]byte, string, error) }`
- `V2RayRenderer`, `ClashRenderer`, `SingBoxRenderer` implementations
- `DetectFormat(userAgent string) string`

**`internal/panel/subscriptions/ratelimit.go`** (new):
- In-memory sliding window rate limiter
- `type RateLimiter interface { Allow(token string) bool }`
- Keyed by token, 10 req/min window

**`internal/panel/httpapi/subscribe.go`** (new):
- `GET /api/v1/subscribe/{token}` handler
- Token validation → subject lookup → node/credential aggregation → format detection → render → rate limit

**`internal/panel/httpapi/subjects.go`** (extend):
- Add `subscription_url` to subject JSON responses
- Add `POST /api/v1/subjects/{id}/revoke-token` handler

### 6.2 Database Migration

**`internal/panel/store/migrations/00012_subscriptions.sql`**:
```sql
-- SP4: Subscription delivery
ALTER TABLE subjects ADD COLUMN subscription_token TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idx_subjects_subscription_token ON subjects(subscription_token) WHERE subscription_token != '';
```

### 6.3 Protocol Support

**SP4 supports:**
- Xray/sing-box protocols (VLESS, VMess, Trojan) via SP2 adapters
- v2ray, Clash, sing-box config formats

**SP5/SP6 will extend:**
- OpenVPN (`.ovpn` profiles)
- L2TP/IPsec (not typically subscription-based, may use credential lists)

---

## 7. Testing Strategy

### 7.1 Unit Tests

- Token generation (entropy, uniqueness, URL-safety)
- Rate limiter (sliding window, expiry, per-token isolation)
- Format detection (UA string parsing)
- Renderer output (valid JSON/YAML/base64, schema compliance)

### 7.2 Integration Tests

- Token lifecycle: generate → fetch subscription → revoke → 404
- Multi-node aggregation: subject on 3 nodes → subscription includes all 3
- Expiry/freeze: expired subject → 404, frozen subject → 404
- Rate limiting: 11th request within 1 minute → 429

### 7.3 E2E Tests

- Real subscription fetch via HTTP
- UA-based format selection (inject different User-Agent headers)
- Credential unsealing (verify rendered UUIDs match sealed data)

---

## 8. Acceptance Criteria

SP4 is complete when:

1. ✅ Subjects have stable, revocable subscription tokens
2. ✅ `GET /api/v1/subscribe/{token}` returns valid configs
3. ✅ UA detection selects correct format (v2ray/Clash/sing-box)
4. ✅ Multi-node aggregation works (all serving nodes appear in config)
5. ✅ Rate limiting prevents abuse (10 req/min per token)
6. ✅ Expired/frozen/disabled subjects return 404
7. ✅ Token revocation invalidates old URLs immediately
8. ✅ Admin API includes subscription URLs in responses
9. ✅ All unit, integration, and E2E tests pass
10. ✅ SP1/SP2/SP3 tests remain green (no regressions)

---

## 9. Non-Goals (Deferred to Later SPs)

- OpenVPN `.ovpn` profiles (SP5)
- L2TP/IPsec credentials (SP6)
- Reseller-specific subscription domains (SP8)
- Subscription update push notifications (future)
- QR code rendering in UI (future, client-side can generate from URL)

---

## 10. Open Questions

**Q: Should tokens be rotatable on a schedule?**  
A: No. Manual revocation is sufficient for SP4. Automatic rotation can be added later if needed.

**Q: Should subscription responses be cached?**  
A: No. Configs must reflect real-time node availability and subject state. Caching would introduce staleness.

**Q: What if a subject has no nodes?**  
A: Return `503 Service Unavailable` (temporary condition) rather than `404` (permanent).

---

## 11. Migration Path

**From SP3 to SP4:**
- Existing subjects work unchanged (tokens generated on first access)
- No UI changes required in SP4 (subscription URLs appear in API responses)
- No agent/node changes required (subscription is panel-only)

**Rollback:**
- Drop `subscription_token` column (or leave it unused)
- Remove subscription endpoints from router

---

## 12. Revision History

- **2026-08-19:** Initial SP4 specification
