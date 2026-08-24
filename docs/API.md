# API Documentation

**Version:** 1.0  
**Base URL:** `http://localhost:8080/api/v1`  
**Authentication:** Session-based (cookie)  
**Last Updated:** 2026-08-22  

## Overview

The Antimage Panel API provides programmatic access to VPN node management, user provisioning, traffic monitoring, and system administration. All authenticated endpoints require a valid session cookie obtained via `/api/v1/auth/login`.

## Authentication

### Session-Based Authentication

The API uses session cookies for authentication. After successful login, the server sets a `session` cookie that must be included in subsequent requests.

**Session Properties:**
- Idle timeout: 4 hours
- Absolute timeout: 7 days
- Secure: Yes (HTTPS only in production)
- HttpOnly: Yes
- SameSite: Strict

### Rate Limiting

Authenticated endpoints are rate-limited to 1000 requests per minute per admin account.

**Rate Limit Headers:**
- `X-RateLimit-Limit`: Maximum requests per window
- `X-RateLimit-Remaining`: Requests remaining
- `X-RateLimit-Reset`: Time when limit resets (Unix timestamp)
- `Retry-After`: Seconds to wait (on 429 response)

## API Endpoints

### Authentication

#### Login
```http
POST /api/v1/auth/login
```

Authenticate with username, password, and optional TOTP code.

**Request Body:**
```json
{
  "username": "admin",
  "password": "secure_password",
  "totp": "123456"
}
```

**Response:** 200 OK
```json
{
  "admin_id": 1,
  "username": "admin",
  "role": "super_admin",
  "totp_enrolled": true
}
```

**Errors:**
- `401 Unauthorized` - Invalid credentials
- `429 Too Many Requests` - Rate limited (includes `Retry-After` header)

---

#### Logout
```http
POST /api/v1/auth/logout
```

Invalidate current session.

**Response:** 204 No Content

---

#### Get Current User
```http
GET /api/v1/auth/me
```

Get authenticated user information.

**Response:** 200 OK
```json
{
  "admin_id": 1,
  "username": "admin",
  "role_name": "super_admin",
  "permissions": ["nodes:read", "nodes:write", "subjects:read", "subjects:write"],
  "scopes": {
    "nodes": [1, 2, 3],
    "services": [10, 11]
  }
}
```

---

### Two-Factor Authentication (TOTP)

#### Enroll TOTP
```http
POST /api/v1/auth/totp/enrol
```

Begin TOTP enrollment. Returns QR code data and secret.

**Response:** 200 OK
```json
{
  "secret": "JBSWY3DPEHPK3PXP",
  "qr_code": "otpauth://totp/Antimage:admin?secret=JBSWY3DPEHPK3PXP&issuer=Antimage",
  "recovery_codes": ["ABC123", "DEF456", "GHI789"]
}
```

---

#### Confirm TOTP
```http
POST /api/v1/auth/totp/confirm
```

Confirm TOTP enrollment with verification code.

**Request Body:**
```json
{
  "code": "123456"
}
```

**Response:** 204 No Content

---

#### Disable TOTP
```http
POST /api/v1/auth/totp/disable
```

Disable TOTP for current user.

**Request Body:**
```json
{
  "password": "current_password"
}
```

**Response:** 204 No Content

---

### Nodes

#### List Nodes
```http
GET /api/v1/nodes
```

List all nodes visible to current user (scoped by permissions).

**Response:** 200 OK
```json
{
  "nodes": [
    {
      "id": 1,
      "name": "node-01-us-west",
      "status": "online",
      "location": "US West",
      "ip_address": "203.0.113.10",
      "desired_revision": 5,
      "applied_revision": 5,
      "last_seen_at": 1724379600,
      "created_at": 1720000000
    }
  ]
}
```

---

#### Create Node
```http
POST /api/v1/nodes
```

Create new node registration.

**Request Body:**
```json
{
  "name": "node-02-eu-central",
  "location": "EU Central",
  "tags": ["production", "germany"]
}
```

**Response:** 201 Created
```json
{
  "id": 2,
  "name": "node-02-eu-central",
  "enrollment_token": "tok_abc123...",
  "enrollment_expires_at": 1724381400
}
```

**Permissions Required:** `nodes:write`

---

#### Get Node
```http
GET /api/v1/nodes/{nodeID}
```

Get node details by ID.

**Response:** 200 OK
```json
{
  "id": 1,
  "name": "node-01-us-west",
  "status": "online",
  "location": "US West",
  "ip_address": "203.0.113.10",
  "desired_revision": 5,
  "applied_revision": 5,
  "last_seen_at": 1724379600,
  "created_at": 1720000000,
  "services_count": 3,
  "subjects_count": 150
}
```

**Permissions Required:** `nodes:read`

---

#### Delete Node
```http
DELETE /api/v1/nodes/{nodeID}
```

Delete node and all associated services.

**Response:** 204 No Content

**Permissions Required:** `nodes:write`

---

#### Issue Enrollment Token
```http
POST /api/v1/nodes/{nodeID}/enroll-token
```

Generate enrollment token for node agent setup.

**Response:** 200 OK
```json
{
  "token": "tok_abc123def456...",
  "expires_at": 1724381400
}
```

**Permissions Required:** `nodes:write`

---

#### List Node Revisions
```http
GET /api/v1/nodes/{nodeID}/revisions
```

List desired state revision history for node.

**Response:** 200 OK
```json
{
  "revisions": [
    {
      "revision": 5,
      "created_at": 1724379000,
      "created_by": "admin",
      "reason": "Added service: Xray VLESS"
    }
  ]
}
```

---

#### Get Node Metrics
```http
GET /api/v1/nodes/{nodeID}/metrics
```

Get current connection and traffic metrics for node.

**Response:** 200 OK
```json
{
  "connections": {
    "active": 45,
    "total_24h": 1203
  },
  "traffic": {
    "uplink_bytes": 5368709120,
    "downlink_bytes": 21474836480
  },
  "timestamp": 1724379600
}
```

---

#### Get Node Health
```http
GET /api/v1/nodes/{nodeID}/health/latest
```

Get latest health check status.

**Response:** 200 OK
```json
{
  "status": "healthy",
  "checks": {
    "adapters": "pass",
    "connectivity": "pass",
    "disk_space": "pass",
    "memory": "pass"
  },
  "checked_at": 1724379600
}
```

---

#### Restart Node
```http
POST /api/v1/nodes/{nodeID}/restart
```

Request node agent restart.

**Response:** 202 Accepted

**Permissions Required:** `nodes:write`

---

#### Sync Node
```http
POST /api/v1/nodes/{nodeID}/sync
```

Force immediate reconciliation (apply desired state).

**Response:** 202 Accepted

**Permissions Required:** `nodes:write`

---

#### Set Node Maintenance Mode
```http
POST /api/v1/nodes/{nodeID}/maintenance
```

Enable or disable maintenance mode.

**Request Body:**
```json
{
  "enabled": true,
  "reason": "Scheduled maintenance - kernel update"
}
```

**Response:** 204 No Content

**Permissions Required:** `nodes:write`

---

### Services

#### Create Service
```http
POST /api/v1/nodes/{nodeID}/services
```

Create new service on node.

**Request Body:**
```json
{
  "name": "Xray VLESS US-West",
  "adapter_kind": "xray",
  "params": {
    "port": 443,
    "protocol": "vless",
    "network": "tcp",
    "tls": true
  }
}
```

**Response:** 201 Created
```json
{
  "id": 10,
  "name": "Xray VLESS US-West",
  "adapter_kind": "xray",
  "node_id": 1,
  "created_at": 1724379600
}
```

**Permissions Required:** `services:write`

---

#### Update Service
```http
PUT /api/v1/services/{serviceID}
```

Update service configuration.

**Request Body:**
```json
{
  "name": "Xray VLESS US-West (Updated)",
  "params": {
    "port": 8443
  }
}
```

**Response:** 200 OK

**Permissions Required:** `services:write`

---

#### Delete Service
```http
DELETE /api/v1/services/{serviceID}
```

Delete service from node.

**Response:** 204 No Content

**Permissions Required:** `services:write`

---

### Subjects (Users)

#### List Subjects
```http
GET /api/v1/subjects
```

List all subjects.

**Response:** 200 OK
```json
{
  "subjects": [
    {
      "id": 1,
      "name": "user@example.com",
      "enabled": true,
      "expires_at": 1727971200,
      "quota_bytes": 107374182400,
      "quota_used_bytes": 53687091200,
      "created_at": 1720000000
    }
  ]
}
```

**Permissions Required:** `subjects:read`

---

#### List Subjects V2 (Paginated)
```http
GET /api/v2/subjects?page=1&page_size=50&search=user&status=active
```

List subjects with pagination, search, and filtering. Results are scoped to
the caller's tenant, so a reseller sees only their own customers.

**Query Parameters:**
- `page` (int): Page number (default: 1)
- `page_size` (int): Items per page (default: 50, max: 1000)
- `search` (string): Substring match against name or note
- `status` (string): `active`, `disabled`, `frozen`, or `expired`
- `expires_before`, `expires_after` (date, `YYYY-MM-DD`)
- `traffic_min`, `traffic_max` (int): bounds on `quota_used_bytes`
- `quota_status` (string): `under_limit`, `near_limit` (80%+), or `over_limit`
- `tag` (string): Substring match against note
- `sort` (string): `name`, `created`, `expires`, `traffic`, or `quota`
- `order` (string): `asc` or `desc` (default: desc)

**Response:** 200 OK
```json
{
  "subjects": [
    {
      "id": 1,
      "name": "alice",
      "enabled": true,
      "expires_at": null,
      "expired_at": null,
      "created_at": 1700000000,
      "note": ""
    }
  ],
  "total": 1523,
  "page": 1,
  "page_size": 50
}
```

Credentials, including `subscription_token`, are never returned by this
endpoint. Reveal one through its own audited route.

**Permissions Required:** `subject:read`

---

#### Create Subject
```http
POST /api/v1/subjects
```

Create new subject with credentials.

**Request Body:**
```json
{
  "name": "newuser@example.com",
  "note": "Premium user - annual plan",
  "expires_at": 1756507200,
  "quota_bytes": 1099511627776,
  "service_ids": [10, 11, 12]
}
```

**Response:** 201 Created
```json
{
  "id": 150,
  "name": "newuser@example.com",
  "created_at": 1724379600,
  "credentials": {
    "uuid": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "password": "generated_password_here"
  }
}
```

**Permissions Required:** `subjects:write`

---

#### Get Subject
```http
GET /api/v1/subjects/{subjectID}
```

Get subject details (credentials NOT included).

**Response:** 200 OK
```json
{
  "id": 1,
  "name": "user@example.com",
  "enabled": true,
  "expires_at": 1727971200,
  "quota_bytes": 107374182400,
  "quota_used_bytes": 53687091200,
  "services": [10, 11],
  "devices": 3,
  "created_at": 1720000000
}
```

**Permissions Required:** `subjects:read`

---

#### Update Subject
```http
PUT /api/v1/subjects/{subjectID}
```

Update subject properties.

**Request Body:**
```json
{
  "name": "updated@example.com",
  "quota_bytes": 214748364800,
  "expires_at": 1730563200
}
```

**Response:** 200 OK

**Permissions Required:** `subjects:write`

---

#### Delete Subject
```http
DELETE /api/v1/subjects/{subjectID}
```

Delete subject and revoke all access.

**Response:** 204 No Content

**Permissions Required:** `subjects:write`

---

#### Reveal Credential
```http
GET /api/v1/subjects/{subjectID}/credentials/{kind}
```

Reveal specific credential (audited action).

**Path Parameters:**
- `kind`: `uuid` or `password`

**Response:** 200 OK
```json
{
  "kind": "uuid",
  "value": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
  "rotation": 0,
  "created_at": 1720000000
}
```

**Permissions Required:** `subjects:credentials:read`

---

#### Rotate Credential
```http
POST /api/v1/subjects/{subjectID}/credentials/{kind}/rotate
```

Generate new credential, invalidating the old one.

**Response:** 200 OK
```json
{
  "kind": "uuid",
  "value": "new-uuid-here",
  "rotation": 1
}
```

**Permissions Required:** `subjects:write`

---

#### Freeze Subject
```http
POST /api/v1/subjects/{subjectID}/freeze
```

Freeze subject (block access, preserve data).

**Request Body:**
```json
{
  "reason": "Payment overdue"
}
```

**Response:** 204 No Content

**Permissions Required:** `subjects:write`

---

#### Unfreeze Subject
```http
POST /api/v1/subjects/{subjectID}/unfreeze
```

Unfreeze subject (restore access).

**Response:** 204 No Content

**Permissions Required:** `subjects:write`

---

### Bulk Operations

#### Bulk Delete Subjects
```http
POST /api/v1/subjects/bulk/delete
```

Delete multiple subjects.

**Request Body:**
```json
{
  "subject_ids": [10, 11, 12]
}
```

**Response:** 200 OK
```json
{
  "deleted": 3,
  "failed": []
}
```

**Permissions Required:** `subjects:write`

---

#### Bulk Set Quota
```http
POST /api/v1/subjects/bulk/set-quota
```

Set quota for multiple subjects.

**Request Body:**
```json
{
  "subject_ids": [10, 11, 12],
  "quota_bytes": 107374182400
}
```

**Response:** 200 OK
```json
{
  "updated": 3
}
```

**Permissions Required:** `subjects:write`

---

### Devices & Enforcement

#### List Devices
```http
GET /api/v1/subjects/{id}/devices
```

List devices for subject.

**Response:** 200 OK
```json
{
  "devices": [
    {
      "id": 1,
      "fingerprint": "device-abc123",
      "last_seen_at": 1724379600,
      "last_ip": "192.0.2.10",
      "created_at": 1720000000
    }
  ]
}
```

---

#### Revoke Device
```http
POST /api/v1/devices/{id}/revoke
```

Revoke device access for subject.

**Response:** 204 No Content

**Permissions Required:** `subjects:write`

---

#### Get Enforcement Status
```http
GET /api/v1/subjects/{id}/enforcement
```

Get real-time enforcement status.

**Response:** 200 OK
```json
{
  "subject_id": 1,
  "active_connections": 2,
  "connection_limit": 5,
  "quota_used_bytes": 53687091200,
  "quota_bytes": 107374182400,
  "quota_utilization": 50.0,
  "frozen": false,
  "violations": []
}
```

---

### Dashboard

#### Dashboard Overview
```http
GET /api/v1/dashboard/overview
```

Get dashboard summary statistics.

**Response:** 200 OK
```json
{
  "nodes": {
    "total": 5,
    "online": 4,
    "degraded": 1,
    "offline": 0
  },
  "subjects": {
    "total": 1523,
    "active": 1450,
    "expired": 50,
    "frozen": 23
  },
  "traffic_24h": {
    "uplink_bytes": 536870912000,
    "downlink_bytes": 2147483648000
  },
  "quota": {
    "total_bytes": 163577856000000,
    "used_bytes": 81788928000000,
    "utilization_pct": 50.0
  },
  "computed_at": 1724379600
}
```

---

#### Dashboard Stream (SSE)
```http
GET /api/v1/dashboard/stream
```

Server-Sent Events stream for real-time dashboard updates.

**Response:** 200 OK (streaming)
```
Content-Type: text/event-stream

event: metrics
data: {"nodes_online":4,"subjects_active":1450}

event: alert
data: {"type":"quota_warning","subject_id":123}
```

---

### Alerts

#### List Alerts
```http
GET /api/v1/alerts?status=active&page=1&limit=50
```

List system alerts.

**Query Parameters:**
- `status`: `active`, `resolved`, `all`
- `severity`: `critical`, `warning`, `info`
- `page`, `limit`: Pagination

**Response:** 200 OK
```json
{
  "alerts": [
    {
      "id": 1,
      "type": "quota_exceeded",
      "severity": "warning",
      "subject_id": 123,
      "message": "Subject exceeded quota (105%)",
      "created_at": 1724379000,
      "resolved_at": null
    }
  ]
}
```

---

### Audit Log

#### List Audit Entries
```http
GET /api/v1/audit?page=1&limit=100&action=subject.create
```

List audit log entries.

**Query Parameters:**
- `action`: Filter by action type
- `admin_id`: Filter by admin
- `page`, `limit`: Pagination

**Response:** 200 OK
```json
{
  "entries": [
    {
      "id": 5000,
      "timestamp": 1724379600,
      "admin_id": 1,
      "admin_username": "admin",
      "action": "subject.create",
      "target_type": "subject",
      "target_id": 150,
      "result": "success",
      "ip_address": "192.0.2.5"
    }
  ]
}
```

---

### Public Endpoints

#### CA Fingerprint
```http
GET /api/v1/ca-fingerprint
```

Get panel CA certificate fingerprint for node enrollment.

**Authentication:** None required

**Response:** 200 OK
```json
{
  "fingerprint": "SHA256:abc123def456..."
}
```

---

#### Subscription
```http
GET /api/v1/subscribe/{token}
```

Get subscription configuration for VPN clients.

**Authentication:** Token-based (no session required)

**Response:** 200 OK (V2Ray JSON or Clash YAML)

---

#### Install Script
```http
GET /install.sh
```

Get node installation script.

**Authentication:** None required

**Response:** 200 OK (bash script)

---

### Health & Monitoring

#### Health Check
```http
GET /health
```

Basic health check.

**Authentication:** None required

**Response:** 200 OK
```json
{
  "status": "ok"
}
```

---

#### Readiness Check
```http
GET /ready
```

Readiness check (database connectivity).

**Authentication:** None required

**Response:** 200 OK
```json
{
  "status": "ready",
  "database": "ok"
}
```

---

#### Prometheus Metrics
```http
GET /metrics
```

Prometheus metrics endpoint.

**Authentication:** None required

**Response:** 200 OK (Prometheus text format)

---

## Error Responses

All errors follow a consistent format:

```json
{
  "error": {
    "code": "error_code",
    "message": "Human-readable error message"
  },
  "request_id": "req_abc123"
}
```

### HTTP Status Codes

- `200 OK` - Success
- `201 Created` - Resource created
- `204 No Content` - Success (no response body)
- `400 Bad Request` - Invalid request
- `401 Unauthorized` - Authentication required
- `403 Forbidden` - Permission denied
- `404 Not Found` - Resource not found
- `409 Conflict` - Resource conflict (e.g., duplicate name)
- `429 Too Many Requests` - Rate limited
- `500 Internal Server Error` - Server error

### Error Codes

- `bad_request` - Malformed request
- `unauthenticated` - Authentication required
- `invalid_credentials` - Wrong username/password/TOTP
- `rate_limited` - Too many requests
- `permission_denied` - Insufficient permissions
- `not_found` - Resource not found
- `conflict` - Resource already exists
- `internal` - Internal server error

---

## Permissions

RBAC permissions control access to endpoints:

| Permission | Allows |
|------------|--------|
| `nodes:read` | List/view nodes |
| `nodes:write` | Create/update/delete nodes |
| `services:read` | List/view services |
| `services:write` | Create/update/delete services |
| `subjects:read` | List/view subjects |
| `subjects:write` | Create/update/delete subjects |
| `subjects:credentials:read` | Reveal credentials |
| `audit:read` | View audit log |
| `admins:read` | List/view admins |
| `admins:write` | Create/update/delete admins |
| `sessions:read` | View sessions |
| `sessions:write` | Revoke sessions |
| `metrics:read` | View metrics |

**Built-in Roles:**
- `super_admin`: All permissions
- `admin`: All except admin management
- `readonly`: Read-only access

---

## Rate Limiting

**Limits:**
- Login: 5 attempts per 15 minutes per IP
- TOTP: 5 attempts per 5 minutes per admin
- General API: 1000 requests per minute per admin

**Response on Rate Limit:**
```json
HTTP/1.1 429 Too Many Requests
Retry-After: 60

{
  "error": {
    "code": "rate_limited",
    "message": "Too many requests; try again later"
  }
}
```

---

## Changelog

### Version 1.0 (2026-08-22)
- Initial API documentation
- Complete endpoint inventory
- Authentication and RBAC documented
- Error format standardized

---

**Note:** This documentation covers the REST API. For gRPC control plane API (agent communication), see separate documentation.
