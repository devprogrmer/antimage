# Premium Management Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a luxury dashboard layer with bulk operations, templates/presets, and wizard workflows on top of the existing antimage engine.

**Architecture:** Keep all existing backend infrastructure intact. Add new API endpoints for dashboard aggregations, bulk operations, templates/presets, and wizards. Extend database schema with new tables. Rebuild frontend in Next.js with shadcn/ui for professional, dashboard-first UX.

**Tech Stack:** 
- Backend: Go 1.26+, chi router, SQLite with goose migrations
- Frontend: Next.js 15, React 19, Tailwind CSS 4, shadcn/ui, TanStack Query, Recharts
- Testing: Go test with -race, Playwright for E2E

## Global Constraints

- Go version: 1.26 or newer
- Node.js version: 20+
- Database: SQLite in WAL mode, foreign keys ON, busy timeout 5000ms
- Migration tool: goose v3
- HTTP router: chi v5
- All timestamps: Unix epoch (INTEGER)
- All JSON columns: TEXT type with validation
- Sequential bulk operations with configurable batch size (default: 10)
- Template variables: `{{GENERATE_*}}` syntax only (no complex expressions)
- Dashboard stats staleness: 60 seconds
- Commit messages: Conventional Commits format (`feat:`, `fix:`, `test:`, `docs:`)
- No breaking changes to existing `/api/v1/*` endpoints

---

## File Structure

### Backend (Go)

**New files to create:**
```
internal/panel/
├── dashboard/
│   ├── stats.go              # Dashboard stats computation and caching
│   ├── stats_test.go
│   ├── sweeper.go            # Background sweeper for materialized stats
│   └── sweeper_test.go
├── templates/
│   ├── service_templates.go  # Service template CRUD
│   ├── service_templates_test.go
│   ├── user_presets.go       # User preset CRUD
│   └── user_presets_test.go
├── bulk/
│   ├── operations.go         # Bulk operation orchestration
│   ├── operations_test.go
│   ├── worker.go             # Background worker for processing
│   └── worker_test.go
├── httpapi/
│   ├── dashboard.go          # Dashboard aggregation endpoints
│   ├── dashboard_test.go
│   ├── templates.go          # Template/preset endpoints
│   ├── templates_test.go
│   ├── bulk.go               # Bulk operation endpoints
│   ├── bulk_test.go
│   ├── wizards.go            # Wizard helper endpoints
│   └── wizards_test.go
├── store/
│   └── migrations/
│       └── 00016_premium_layer.sql  # New schema
```

**Files to modify:**
```
internal/panel/
├── httpapi/
│   └── server.go             # Mount new routes
└── store/
    └── store.go              # Add new query methods
cmd/antimage-panel/
└── main.go                   # Start dashboard sweeper and bulk worker
```

### Frontend (Next.js)

**New directory structure:**
```
web-next/                     # New Next.js app
├── app/
│   ├── (auth)/
│   │   ├── login/
│   │   │   └── page.tsx
│   │   └── layout.tsx
│   ├── (dashboard)/
│   │   ├── layout.tsx
│   │   ├── page.tsx          # Dashboard overview
│   │   ├── nodes/
│   │   │   ├── page.tsx
│   │   │   ├── [id]/page.tsx
│   │   │   └── new/page.tsx  # Wizard
│   │   ├── users/
│   │   │   ├── page.tsx
│   │   │   ├── [id]/page.tsx
│   │   │   └── new/page.tsx  # Wizard
│   │   ├── templates/
│   │   │   ├── services/page.tsx
│   │   │   └── users/page.tsx
│   │   ├── bulk-operations/
│   │   │   └── [id]/page.tsx
│   │   └── settings/
│   │       └── page.tsx
│   ├── layout.tsx
│   └── globals.css
├── components/
│   ├── dashboard/
│   │   ├── stats-card.tsx
│   │   ├── traffic-chart.tsx
│   │   ├── node-status-grid.tsx
│   │   └── top-users-widget.tsx
│   ├── tables/
│   │   ├── data-table.tsx
│   │   └── bulk-action-bar.tsx
│   ├── wizards/
│   │   ├── node-onboarding.tsx
│   │   └── user-onboarding.tsx
│   └── ui/                   # shadcn/ui components
│       ├── button.tsx
│       ├── card.tsx
│       ├── table.tsx
│       └── ...
├── hooks/
│   ├── use-sse.ts
│   ├── use-wizard.ts
│   └── use-bulk-operation.ts
├── lib/
│   ├── api.ts
│   ├── auth.ts
│   └── utils.ts
├── package.json
├── tailwind.config.ts
├── tsconfig.json
└── next.config.ts
```

---

## Task 1: Database Schema - Premium Layer Tables

**Files:**
- Create: `internal/panel/store/migrations/00016_premium_layer.sql`

**Interfaces:**
- Consumes: Existing `nodes`, `subjects`, `services`, `admins` tables
- Produces: New tables `service_templates`, `user_presets`, `bulk_operations`, `dashboard_stats`, plus `nodes.tags_json` column

- [ ] **Step 1: Write migration with service_templates table**

Create `internal/panel/store/migrations/00016_premium_layer.sql`:

```sql
-- +goose Up
-- Premium Management Layer: Templates, Presets, Bulk Operations, Dashboard Stats

-- Service Templates: Reusable protocol configurations
CREATE TABLE service_templates (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    adapter_kind TEXT NOT NULL CHECK (adapter_kind IN ('xray','singbox','openvpn','l2tp')),
    params_json TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    tags_json TEXT NOT NULL DEFAULT '[]',
    is_public INTEGER NOT NULL DEFAULT 0 CHECK (is_public IN (0,1)),
    created_by INTEGER REFERENCES admins(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX service_templates_adapter ON service_templates(adapter_kind);
CREATE INDEX service_templates_creator ON service_templates(created_by);

-- User Presets: Common quota/expiry patterns
CREATE TABLE user_presets (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT NOT NULL DEFAULT '',
    quota_bytes INTEGER,
    validity_days INTEGER,
    auto_assign_services_json TEXT NOT NULL DEFAULT '[]',
    auto_assign_node_tags_json TEXT NOT NULL DEFAULT '[]',
    is_public INTEGER NOT NULL DEFAULT 0 CHECK (is_public IN (0,1)),
    created_by INTEGER REFERENCES admins(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX user_presets_creator ON user_presets(created_by);

-- Bulk Operations: Track batch job progress
CREATE TABLE bulk_operations (
    id INTEGER PRIMARY KEY,
    operation_type TEXT NOT NULL CHECK (operation_type IN (
        'subjects_create',
        'subjects_update',
        'subjects_delete',
        'subjects_freeze',
        'subjects_unfreeze',
        'subjects_grant_service',
        'subjects_revoke_service',
        'nodes_configure',
        'nodes_update_settings'
    )),
    actor_admin_id INTEGER REFERENCES admins(id) ON DELETE SET NULL,
    total_items INTEGER NOT NULL CHECK (total_items > 0),
    completed_items INTEGER NOT NULL DEFAULT 0 CHECK (completed_items >= 0),
    failed_items INTEGER NOT NULL DEFAULT 0 CHECK (failed_items >= 0),
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN (
        'queued','running','completed','failed','cancelled'
    )),
    results_json TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    completed_at INTEGER,
    CHECK (completed_items + failed_items <= total_items)
) STRICT;

CREATE INDEX bulk_operations_actor ON bulk_operations(actor_admin_id, created_at DESC);
CREATE INDEX bulk_operations_status ON bulk_operations(status, created_at);

-- Dashboard Stats: Materialized aggregates
CREATE TABLE dashboard_stats (
    admin_id INTEGER REFERENCES admins(id) ON DELETE CASCADE,
    computed_at INTEGER NOT NULL,
    nodes_total INTEGER NOT NULL DEFAULT 0,
    nodes_online INTEGER NOT NULL DEFAULT 0,
    nodes_degraded INTEGER NOT NULL DEFAULT 0,
    nodes_offline INTEGER NOT NULL DEFAULT 0,
    subjects_total INTEGER NOT NULL DEFAULT 0,
    subjects_active INTEGER NOT NULL DEFAULT 0,
    subjects_expired INTEGER NOT NULL DEFAULT 0,
    subjects_frozen INTEGER NOT NULL DEFAULT 0,
    traffic_24h_uplink INTEGER NOT NULL DEFAULT 0,
    traffic_24h_downlink INTEGER NOT NULL DEFAULT 0,
    quota_total_bytes INTEGER,
    quota_used_bytes INTEGER NOT NULL DEFAULT 0,
    quota_utilization_pct REAL,
    PRIMARY KEY (admin_id)
) STRICT;

CREATE INDEX dashboard_stats_computed ON dashboard_stats(computed_at);

-- Extend nodes with tags
ALTER TABLE nodes ADD COLUMN tags_json TEXT NOT NULL DEFAULT '[]';
CREATE INDEX nodes_tags ON nodes(tags_json) WHERE tags_json != '[]';

-- +goose Down
DROP INDEX IF EXISTS nodes_tags;
ALTER TABLE nodes DROP COLUMN tags_json;
DROP TABLE dashboard_stats;
DROP TABLE bulk_operations;
DROP TABLE user_presets;
DROP TABLE service_templates;
```

- [ ] **Step 2: Test migration applies cleanly**

Run: `cd internal/panel/store && go test -run TestMigrations -v`

Expected: Migration 00016 applies without errors, schema matches expectations

- [ ] **Step 3: Verify schema with SQLite inspection**

Run:
```bash
sqlite3 /tmp/test-antimage.db ".schema service_templates"
sqlite3 /tmp/test-antimage.db ".schema user_presets"
sqlite3 /tmp/test-antimage.db ".schema bulk_operations"
sqlite3 /tmp/test-antimage.db ".schema dashboard_stats"
```

Expected: All tables exist with correct columns and constraints

- [ ] **Step 4: Commit**

```bash
git add internal/panel/store/migrations/00016_premium_layer.sql
git commit -m "feat(premium): add database schema for templates, presets, bulk ops, dashboard stats"
```

---

## Task 2: Service Templates - Backend Logic

**Files:**
- Create: `internal/panel/templates/service_templates.go`
- Create: `internal/panel/templates/service_templates_test.go`

**Interfaces:**
- Consumes: `store.Store`, `rbac.Actor`, `adapter.Descriptor` (for validation)
- Produces: 
  - `type ServiceTemplate struct { ID int64; Name string; AdapterKind string; ParamsJSON string; Description string; Tags []string; IsPublic bool; CreatedBy *int64; CreatedAt, UpdatedAt int64 }`
  - `func ListTemplates(ctx context.Context, db *store.Store, actor rbac.Actor, filters TemplateFilters) ([]ServiceTemplate, error)`
  - `func CreateTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, input CreateTemplateInput) (ServiceTemplate, error)`
  - `func GetTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) (ServiceTemplate, error)`
  - `func UpdateTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, id int64, input UpdateTemplateInput) error`
  - `func DeleteTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) error`

- [ ] **Step 1: Write failing test for ListTemplates**

Create `internal/panel/templates/service_templates_test.go`:

```go
package templates

import (
	"context"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func TestListTemplates_Empty(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	templates, err := ListTemplates(context.Background(), db, actor, TemplateFilters{})
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}
	if len(templates) != 0 {
		t.Errorf("expected 0 templates, got %d", len(templates))
	}
}

func TestCreateTemplate_ValidXray(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	input := CreateTemplateInput{
		Name:        "Xray VLESS Production",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":443,"protocol":"vless"}`,
		Description: "Production VLESS config",
		Tags:        []string{"production", "vless"},
		IsPublic:    true,
	}
	
	tmpl, err := CreateTemplate(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}
	if tmpl.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if tmpl.Name != input.Name {
		t.Errorf("expected name %q, got %q", input.Name, tmpl.Name)
	}
	if len(tmpl.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(tmpl.Tags))
	}
}

func TestCreateTemplate_DuplicateName(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	input := CreateTemplateInput{
		Name:        "Duplicate Template",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":443}`,
	}
	
	_, err := CreateTemplate(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("first CreateTemplate failed: %v", err)
	}
	
	_, err = CreateTemplate(context.Background(), db, actor, input)
	if err == nil {
		t.Error("expected error for duplicate name, got nil")
	}
}

func TestListTemplates_ScopeFiltering(t *testing.T) {
	db := store.OpenTestDB(t)
	admin1 := rbac.Actor{AdminID: 1, Role: rbac.RoleAdmin, Scope: rbac.ScopeAll()}
	admin2 := rbac.Actor{AdminID: 2, Role: rbac.RoleAdmin, Scope: rbac.ScopeAll()}
	
	// Admin 1 creates a private template
	input := CreateTemplateInput{
		Name:        "Private Template",
		AdapterKind: "xray",
		ParamsJSON:  `{"listen_port":8080}`,
		IsPublic:    false,
	}
	_, err := CreateTemplate(context.Background(), db, admin1, input)
	if err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}
	
	// Admin 2 should NOT see it
	templates, err := ListTemplates(context.Background(), db, admin2, TemplateFilters{})
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}
	if len(templates) != 0 {
		t.Errorf("admin2 should not see admin1's private template, got %d templates", len(templates))
	}
	
	// Admin 1 should see it
	templates, err = ListTemplates(context.Background(), db, admin1, TemplateFilters)
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}
	if len(templates) != 1 {
		t.Errorf("admin1 should see own template, got %d templates", len(templates))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestListTemplates ./internal/panel/templates -v`

Expected: FAIL with "undefined: ListTemplates"

- [ ] **Step 3: Implement minimal service_templates.go**

Create `internal/panel/templates/service_templates.go`:

```go
package templates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type ServiceTemplate struct {
	ID          int64
	Name        string
	AdapterKind string
	ParamsJSON  string
	Description string
	Tags        []string
	IsPublic    bool
	CreatedBy   *int64
	CreatedAt   int64
	UpdatedAt   int64
}

type TemplateFilters struct {
	AdapterKind string
	Tags        []string
}

type CreateTemplateInput struct {
	Name        string
	AdapterKind string
	ParamsJSON  string
	Description string
	Tags        []string
	IsPublic    bool
}

type UpdateTemplateInput struct {
	Name        *string
	ParamsJSON  *string
	Description *string
	Tags        *[]string
	IsPublic    *bool
}

func ListTemplates(ctx context.Context, db *store.Store, actor rbac.Actor, filters TemplateFilters) ([]ServiceTemplate, error) {
	query := `
		SELECT id, name, adapter_kind, params_json, description, tags_json, is_public, created_by, created_at, updated_at
		FROM service_templates
		WHERE is_public = 1 OR created_by = ?
	`
	args := []interface{}{actor.AdminID}
	
	if filters.AdapterKind != "" {
		query += " AND adapter_kind = ?"
		args = append(args, filters.AdapterKind)
	}
	
	query += " ORDER BY name"
	
	rows, err := db.ReadDB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query templates: %w", err)
	}
	defer rows.Close()
	
	var templates []ServiceTemplate
	for rows.Next() {
		var t ServiceTemplate
		var tagsJSON string
		if err := rows.Scan(&t.ID, &t.Name, &t.AdapterKind, &t.ParamsJSON, &t.Description, &tagsJSON, &t.IsPublic, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &t.Tags); err != nil {
			return nil, fmt.Errorf("unmarshal tags: %w", err)
		}
		templates = append(templates, t)
	}
	
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate templates: %w", err)
	}
	
	return templates, nil
}

func CreateTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, input CreateTemplateInput) (ServiceTemplate, error) {
	if input.Name == "" {
		return ServiceTemplate{}, errors.New("template name is required")
	}
	if input.AdapterKind == "" {
		return ServiceTemplate{}, errors.New("adapter kind is required")
	}
	if input.ParamsJSON == "" {
		return ServiceTemplate{}, errors.New("params_json is required")
	}
	
	// Validate JSON
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(input.ParamsJSON), &params); err != nil {
		return ServiceTemplate{}, fmt.Errorf("invalid params_json: %w", err)
	}
	
	tagsJSON, err := json.Marshal(input.Tags)
	if err != nil {
		return ServiceTemplate{}, fmt.Errorf("marshal tags: %w", err)
	}
	
	now := time.Now().Unix()
	
	result, err := db.WriteDB().ExecContext(ctx, `
		INSERT INTO service_templates (name, adapter_kind, params_json, description, tags_json, is_public, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, input.Name, input.AdapterKind, input.ParamsJSON, input.Description, string(tagsJSON), input.IsPublic, actor.AdminID, now, now)
	if err != nil {
		return ServiceTemplate{}, fmt.Errorf("insert template: %w", err)
	}
	
	id, err := result.LastInsertId()
	if err != nil {
		return ServiceTemplate{}, fmt.Errorf("get insert id: %w", err)
	}
	
	return ServiceTemplate{
		ID:          id,
		Name:        input.Name,
		AdapterKind: input.AdapterKind,
		ParamsJSON:  input.ParamsJSON,
		Description: input.Description,
		Tags:        input.Tags,
		IsPublic:    input.IsPublic,
		CreatedBy:   &actor.AdminID,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func GetTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) (ServiceTemplate, error) {
	var t ServiceTemplate
	var tagsJSON string
	err := db.ReadDB().QueryRowContext(ctx, `
		SELECT id, name, adapter_kind, params_json, description, tags_json, is_public, created_by, created_at, updated_at
		FROM service_templates
		WHERE id = ? AND (is_public = 1 OR created_by = ?)
	`, id, actor.AdminID).Scan(&t.ID, &t.Name, &t.AdapterKind, &t.ParamsJSON, &t.Description, &tagsJSON, &t.IsPublic, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceTemplate{}, fmt.Errorf("template not found or access denied")
	}
	if err != nil {
		return ServiceTemplate{}, fmt.Errorf("query template: %w", err)
	}
	
	if err := json.Unmarshal([]byte(tagsJSON), &t.Tags); err != nil {
		return ServiceTemplate{}, fmt.Errorf("unmarshal tags: %w", err)
	}
	
	return t, nil
}

func UpdateTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, id int64, input UpdateTemplateInput) error {
	// Check ownership
	var createdBy sql.NullInt64
	err := db.ReadDB().QueryRowContext(ctx, "SELECT created_by FROM service_templates WHERE id = ?", id).Scan(&createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("template not found")
	}
	if err != nil {
		return fmt.Errorf("query template: %w", err)
	}
	
	if !createdBy.Valid || createdBy.Int64 != actor.AdminID {
		if actor.Role != rbac.RoleSuperAdmin {
			return fmt.Errorf("cannot modify another admin's template")
		}
	}
	
	updates := []string{}
	args := []interface{}{}
	
	if input.Name != nil {
		updates = append(updates, "name = ?")
		args = append(args, *input.Name)
	}
	if input.ParamsJSON != nil {
		// Validate JSON
		var params map[string]interface{}
		if err := json.Unmarshal([]byte(*input.ParamsJSON), &params); err != nil {
			return fmt.Errorf("invalid params_json: %w", err)
		}
		updates = append(updates, "params_json = ?")
		args = append(args, *input.ParamsJSON)
	}
	if input.Description != nil {
		updates = append(updates, "description = ?")
		args = append(args, *input.Description)
	}
	if input.Tags != nil {
		tagsJSON, err := json.Marshal(*input.Tags)
		if err != nil {
			return fmt.Errorf("marshal tags: %w", err)
		}
		updates = append(updates, "tags_json = ?")
		args = append(args, string(tagsJSON))
	}
	if input.IsPublic != nil {
		updates = append(updates, "is_public = ?")
		args = append(args, *input.IsPublic)
	}
	
	if len(updates) == 0 {
		return nil // no-op
	}
	
	updates = append(updates, "updated_at = ?")
	args = append(args, time.Now().Unix())
	args = append(args, id)
	
	query := "UPDATE service_templates SET " + updates[0]
	for i := 1; i < len(updates); i++ {
		query += ", " + updates[i]
	}
	query += " WHERE id = ?"
	
	_, err = db.WriteDB().ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update template: %w", err)
	}
	
	return nil
}

func DeleteTemplate(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) error {
	// Check ownership
	var createdBy sql.NullInt64
	err := db.ReadDB().QueryRowContext(ctx, "SELECT created_by FROM service_templates WHERE id = ?", id).Scan(&createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("template not found")
	}
	if err != nil {
		return fmt.Errorf("query template: %w", err)
	}
	
	if !createdBy.Valid || createdBy.Int64 != actor.AdminID {
		if actor.Role != rbac.RoleSuperAdmin {
			return fmt.Errorf("cannot delete another admin's template")
		}
	}
	
	_, err = db.WriteDB().ExecContext(ctx, "DELETE FROM service_templates WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete template: %w", err)
	}
	
	return nil
}
```

- [ ] **Step 4: Add ReadDB/WriteDB accessors to store.Store**

Modify `internal/panel/store/store.go`, add these methods after the `Open` function:

```go
// ReadDB returns the pooled read handle for concurrent queries.
func (s *Store) ReadDB() *sql.DB {
	return s.read
}

// WriteDB returns the serialized write handle for mutations.
func (s *Store) WriteDB() *sql.DB {
	return s.write
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/panel/templates -v -race`

Expected: All tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/panel/templates/
git add internal/panel/store/store.go
git commit -m "feat(premium): implement service template CRUD with scope filtering"
```

---

## Task 3: User Presets - Backend Logic

**Files:**
- Create: `internal/panel/templates/user_presets.go`
- Create: `internal/panel/templates/user_presets_test.go`

**Interfaces:**
- Consumes: `store.Store`, `rbac.Actor`
- Produces:
  - `type UserPreset struct { ID int64; Name string; Description string; QuotaBytes *int64; ValidityDays *int; AutoAssignServicesJSON json.RawMessage; AutoAssignNodeTagsJSON json.RawMessage; IsPublic bool; CreatedBy *int64; CreatedAt, UpdatedAt int64 }`
  - `type CreatePresetInput struct { Name string; Description string; QuotaBytes *int64; ValidityDays *int; AutoAssignServicesJSON json.RawMessage; AutoAssignNodeTagsJSON json.RawMessage; IsPublic bool }`
  - `type UpdatePresetInput struct { Name *string; Description *string; QuotaBytes **int64; ValidityDays **int; AutoAssignServicesJSON *json.RawMessage; AutoAssignNodeTagsJSON *json.RawMessage; IsPublic *bool }`
  - `func ListPresets(ctx context.Context, db *store.Store, actor rbac.Actor) ([]UserPreset, error)`
  - `func CreatePreset(ctx context.Context, db *store.Store, actor rbac.Actor, input CreatePresetInput) (UserPreset, error)` — uses `RETURNING *` inside `db.Write()`
  - `func GetPreset(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) (UserPreset, error)`
  - `func UpdatePreset(ctx context.Context, db *store.Store, actor rbac.Actor, id int64, input UpdatePresetInput) (UserPreset, error)` — returns updated preset via `RETURNING *`
  - `func DeletePreset(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) error`
- **Design note:** JSON array fields use `json.RawMessage` for flexible passthrough — callers marshal/unmarshal at their layer. `CreatePreset` and `UpdatePreset` use SQLite `RETURNING *` inside `db.Write()` transactions instead of `LastInsertId` / re-query patterns.

- [ ] **Step 1: Write failing test for ListPresets**

Create `internal/panel/templates/user_presets_test.go`:

```go
package templates

import (
	"context"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func TestListPresets_Empty(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	presets, err := ListPresets(context.Background(), db, actor)
	if err != nil {
		t.Fatalf("ListPresets failed: %v", err)
	}
	if len(presets) != 0 {
		t.Errorf("expected 0 presets, got %d", len(presets))
	}
}

func TestCreatePreset_ValidStandard(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	quota := int64(53687091200) // 50GB
	validity := 30
	
	input := CreatePresetInput{
		Name:         "Standard User",
		Description:  "50GB quota, 30 days",
		QuotaBytes:   &quota,
		ValidityDays: &validity,
		AutoAssignServices: []int64{1, 2},
		AutoAssignNodeTags: []string{"premium"},
		IsPublic:     true,
	}
	
	preset, err := CreatePreset(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}
	if preset.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if preset.QuotaBytes == nil || *preset.QuotaBytes != quota {
		t.Errorf("expected quota %d, got %v", quota, preset.QuotaBytes)
	}
	if len(preset.AutoAssignServices) != 2 {
		t.Errorf("expected 2 auto-assign services, got %d", len(preset.AutoAssignServices))
	}
}

func TestCreatePreset_UnlimitedQuota(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	input := CreatePresetInput{
		Name:       "Unlimited User",
		QuotaBytes: nil, // unlimited
	}
	
	preset, err := CreatePreset(context.Background(), db, actor, input)
	if err != nil {
		t.Fatalf("CreatePreset failed: %v", err)
	}
	if preset.QuotaBytes != nil {
		t.Errorf("expected nil quota (unlimited), got %v", preset.QuotaBytes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestListPresets ./internal/panel/templates -v`

Expected: FAIL with "undefined: ListPresets"

- [ ] **Step 3: Implement user_presets.go**

Create `internal/panel/templates/user_presets.go`:

```go
package templates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type UserPreset struct {
	ID                  int64
	Name                string
	Description         string
	QuotaBytes          *int64
	ValidityDays        *int
	AutoAssignServices  []int64
	AutoAssignNodeTags  []string
	IsPublic            bool
	CreatedBy           *int64
	CreatedAt           int64
	UpdatedAt           int64
}

type CreatePresetInput struct {
	Name                string
	Description         string
	QuotaBytes          *int64
	ValidityDays        *int
	AutoAssignServices  []int64
	AutoAssignNodeTags  []string
	IsPublic            bool
}

type UpdatePresetInput struct {
	Name                *string
	Description         *string
	QuotaBytes          **int64 // double pointer to distinguish between "set to NULL" and "don't update"
	ValidityDays        **int
	AutoAssignServices  *[]int64
	AutoAssignNodeTags  *[]string
	IsPublic            *bool
}

func ListPresets(ctx context.Context, db *store.Store, actor rbac.Actor) ([]UserPreset, error) {
	query := `
		SELECT id, name, description, quota_bytes, validity_days, auto_assign_services_json, auto_assign_node_tags_json, is_public, created_by, created_at, updated_at
		FROM user_presets
		WHERE is_public = 1 OR created_by = ?
		ORDER BY name
	`
	
	rows, err := db.ReadDB().QueryContext(ctx, query, actor.AdminID)
	if err != nil {
		return nil, fmt.Errorf("query presets: %w", err)
	}
	defer rows.Close()
	
	var presets []UserPreset
	for rows.Next() {
		var p UserPreset
		var servicesJSON, tagsJSON string
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.QuotaBytes, &p.ValidityDays, &servicesJSON, &tagsJSON, &p.IsPublic, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan preset: %w", err)
		}
		if err := json.Unmarshal([]byte(servicesJSON), &p.AutoAssignServices); err != nil {
			return nil, fmt.Errorf("unmarshal services: %w", err)
		}
		if err := json.Unmarshal([]byte(tagsJSON), &p.AutoAssignNodeTags); err != nil {
			return nil, fmt.Errorf("unmarshal tags: %w", err)
		}
		presets = append(presets, p)
	}
	
	return presets, rows.Err()
}

func CreatePreset(ctx context.Context, db *store.Store, actor rbac.Actor, input CreatePresetInput) (UserPreset, error) {
	if input.Name == "" {
		return UserPreset{}, errors.New("preset name is required")
	}
	
	servicesJSON, err := json.Marshal(input.AutoAssignServices)
	if err != nil {
		return UserPreset{}, fmt.Errorf("marshal services: %w", err)
	}
	
	tagsJSON, err := json.Marshal(input.AutoAssignNodeTags)
	if err != nil {
		return UserPreset{}, fmt.Errorf("marshal tags: %w", err)
	}
	
	now := time.Now().Unix()
	
	result, err := db.WriteDB().ExecContext(ctx, `
		INSERT INTO user_presets (name, description, quota_bytes, validity_days, auto_assign_services_json, auto_assign_node_tags_json, is_public, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, input.Name, input.Description, input.QuotaBytes, input.ValidityDays, string(servicesJSON), string(tagsJSON), input.IsPublic, actor.AdminID, now, now)
	if err != nil {
		return UserPreset{}, fmt.Errorf("insert preset: %w", err)
	}
	
	id, err := result.LastInsertId()
	if err != nil {
		return UserPreset{}, fmt.Errorf("get insert id: %w", err)
	}
	
	return UserPreset{
		ID:                 id,
		Name:               input.Name,
		Description:        input.Description,
		QuotaBytes:         input.QuotaBytes,
		ValidityDays:       input.ValidityDays,
		AutoAssignServices: input.AutoAssignServices,
		AutoAssignNodeTags: input.AutoAssignNodeTags,
		IsPublic:           input.IsPublic,
		CreatedBy:          &actor.AdminID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

func GetPreset(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) (UserPreset, error) {
	var p UserPreset
	var servicesJSON, tagsJSON string
	err := db.ReadDB().QueryRowContext(ctx, `
		SELECT id, name, description, quota_bytes, validity_days, auto_assign_services_json, auto_assign_node_tags_json, is_public, created_by, created_at, updated_at
		FROM user_presets
		WHERE id = ? AND (is_public = 1 OR created_by = ?)
	`, id, actor.AdminID).Scan(&p.ID, &p.Name, &p.Description, &p.QuotaBytes, &p.ValidityDays, &servicesJSON, &tagsJSON, &p.IsPublic, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	
	if errors.Is(err, sql.ErrNoRows) {
		return UserPreset{}, fmt.Errorf("preset not found or access denied")
	}
	if err != nil {
		return UserPreset{}, fmt.Errorf("query preset: %w", err)
	}
	
	if err := json.Unmarshal([]byte(servicesJSON), &p.AutoAssignServices); err != nil {
		return UserPreset{}, fmt.Errorf("unmarshal services: %w", err)
	}
	if err := json.Unmarshal([]byte(tagsJSON), &p.AutoAssignNodeTags); err != nil {
		return UserPreset{}, fmt.Errorf("unmarshal tags: %w", err)
	}
	
	return p, nil
}

func UpdatePreset(ctx context.Context, db *store.Store, actor rbac.Actor, id int64, input UpdatePresetInput) error {
	// Check ownership (similar to UpdateTemplate)
	var createdBy sql.NullInt64
	err := db.ReadDB().QueryRowContext(ctx, "SELECT created_by FROM user_presets WHERE id = ?", id).Scan(&createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("preset not found")
	}
	if err != nil {
		return fmt.Errorf("query preset: %w", err)
	}
	
	if !createdBy.Valid || createdBy.Int64 != actor.AdminID {
		if actor.Role != rbac.RoleSuperAdmin {
			return fmt.Errorf("cannot modify another admin's preset")
		}
	}
	
	// Build dynamic update query (similar to UpdateTemplate)
	updates := []string{}
	args := []interface{}{}
	
	if input.Name != nil {
		updates = append(updates, "name = ?")
		args = append(args, *input.Name)
	}
	if input.Description != nil {
		updates = append(updates, "description = ?")
		args = append(args, *input.Description)
	}
	// Handle double pointers for nullable fields
	if input.QuotaBytes != nil {
		updates = append(updates, "quota_bytes = ?")
		args = append(args, *input.QuotaBytes)
	}
	if input.ValidityDays != nil {
		updates = append(updates, "validity_days = ?")
		args = append(args, *input.ValidityDays)
	}
	if input.AutoAssignServices != nil {
		servicesJSON, err := json.Marshal(*input.AutoAssignServices)
		if err != nil {
			return fmt.Errorf("marshal services: %w", err)
		}
		updates = append(updates, "auto_assign_services_json = ?")
		args = append(args, string(servicesJSON))
	}
	if input.AutoAssignNodeTags != nil {
		tagsJSON, err := json.Marshal(*input.AutoAssignNodeTags)
		if err != nil {
			return fmt.Errorf("marshal tags: %w", err)
		}
		updates = append(updates, "auto_assign_node_tags_json = ?")
		args = append(args, string(tagsJSON))
	}
	if input.IsPublic != nil {
		updates = append(updates, "is_public = ?")
		args = append(args, *input.IsPublic)
	}
	
	if len(updates) == 0 {
		return nil
	}
	
	updates = append(updates, "updated_at = ?")
	args = append(args, time.Now().Unix())
	args = append(args, id)
	
	query := "UPDATE user_presets SET " + updates[0]
	for i := 1; i < len(updates); i++ {
		query += ", " + updates[i]
	}
	query += " WHERE id = ?"
	
	_, err = db.WriteDB().ExecContext(ctx, query, args...)
	return err
}

func DeletePreset(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) error {
	// Check ownership (similar to DeleteTemplate)
	var createdBy sql.NullInt64
	err := db.ReadDB().QueryRowContext(ctx, "SELECT created_by FROM user_presets WHERE id = ?", id).Scan(&createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("preset not found")
	}
	if err != nil {
		return fmt.Errorf("query preset: %w", err)
	}
	
	if !createdBy.Valid || createdBy.Int64 != actor.AdminID {
		if actor.Role != rbac.RoleSuperAdmin {
			return fmt.Errorf("cannot delete another admin's preset")
		}
	}
	
	_, err = db.WriteDB().ExecContext(ctx, "DELETE FROM user_presets WHERE id = ?", id)
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/panel/templates -v -race`

Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/panel/templates/user_presets.go internal/panel/templates/user_presets_test.go
git commit -m "feat(premium): implement user preset CRUD with scope filtering"
```

---

## Task 4: Bulk Operations - Core Logic

**Files:**
- Create: `internal/panel/bulk/operations.go`
- Create: `internal/panel/bulk/operations_test.go`
- Create: `internal/panel/bulk/worker.go`
- Create: `internal/panel/bulk/worker_test.go`

**Interfaces:**
- Consumes: `store.Store`, `rbac.Actor`
- Produces:
  - `type BulkStatus string` — typed constants `StatusQueued`, `StatusRunning`, `StatusCompleted`, `StatusFailed`, `StatusCancelled`
  - `type ItemResult struct { ItemID string \`json:"item_id"\`; Status string \`json:"status"\`; Error string \`json:"error,omitempty"\` }` — Status is `"success"` or `"failed"`
  - `type BulkOperation struct { ID int64; OperationType string; ActorAdminID *int64; TotalItems, CompletedItems, FailedItems int; Status BulkStatus; Results []ItemResult; ResultsJSON string; CreatedAt int64; StartedAt, CompletedAt *int64 }` — Results populated for completed/failed ops
  - `func CreateBulkOperation(ctx context.Context, db *store.Store, actor rbac.Actor, opType string, items []interface{}) (*BulkOperation, error)`
  - `func GetBulkOperation(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) (*BulkOperation, error)` — unmarshals Results for completed/failed
  - `func ListBulkOperations(ctx context.Context, db *store.Store, actor rbac.Actor) ([]*BulkOperation, error)` — super sees all, non-super sees own
  - `func CancelBulkOperation(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) error`
  - **Worker (struct-based design — chosen over `StartWorker`/`WorkerDeps` for idiomatic Go):**
    - `const DefaultBatchSize = 10`
    - `type ProcessFunc func(ctx context.Context, kind string, item json.RawMessage) (itemID string, err error)`
    - `type Worker struct { db *store.Store; processFn ProcessFunc; BatchSize int }`
    - `func NewWorker(db *store.Store, processFn ProcessFunc) *Worker` — BatchSize defaults to DefaultBatchSize
    - `func (w *Worker) Run(ctx context.Context)` — polls, backs off on sql.ErrNoRows, processes in batches
  - **Task 9 wiring:** `w := bulk.NewWorker(db, myProcessFn); go w.Run(ctx)` — no `StartWorker` function exists

- [ ] **Step 1: Write failing test for CreateBulkOperation**

Create `internal/panel/bulk/operations_test.go`:

```go
package bulk

import (
	"context"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func TestCreateBulkOperation_SubjectsCreate(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	items := []interface{}{
		map[string]interface{}{"name": "user1@example.com", "quota_bytes": 53687091200},
		map[string]interface{}{"name": "user2@example.com", "quota_bytes": 53687091200},
	}
	
	id, err := CreateBulkOperation(context.Background(), db, actor, "subjects_create", items)
	if err != nil {
		t.Fatalf("CreateBulkOperation failed: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero operation ID")
	}
	
	// Verify operation was created
	op, err := GetBulkOperation(context.Background(), db, actor, id)
	if err != nil {
		t.Fatalf("GetBulkOperation failed: %v", err)
	}
	if op.Status != "queued" {
		t.Errorf("expected status 'queued', got %q", op.Status)
	}
	if op.TotalItems != 2 {
		t.Errorf("expected 2 items, got %d", op.TotalItems)
	}
}

func TestCancelBulkOperation(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	items := []interface{}{
		map[string]interface{}{"name": "user1@example.com"},
	}
	
	id, err := CreateBulkOperation(context.Background(), db, actor, "subjects_create", items)
	if err != nil {
		t.Fatalf("CreateBulkOperation failed: %v", err)
	}
	
	// Cancel it
	err = CancelBulkOperation(context.Background(), db, actor, id)
	if err != nil {
		t.Fatalf("CancelBulkOperation failed: %v", err)
	}
	
	// Verify status changed
	op, err := GetBulkOperation(context.Background(), db, actor, id)
	if err != nil {
		t.Fatalf("GetBulkOperation failed: %v", err)
	}
	if op.Status != "cancelled" {
		t.Errorf("expected status 'cancelled', got %q", op.Status)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestCreateBulkOperation ./internal/panel/bulk -v`

Expected: FAIL with "undefined: CreateBulkOperation"

- [ ] **Step 3: Implement operations.go**

Create `internal/panel/bulk/operations.go`:

```go
package bulk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type BulkOperation struct {
	ID             int64
	OperationType  string
	ActorAdminID   *int64
	TotalItems     int
	CompletedItems int
	FailedItems    int
	Status         string
	Results        []ItemResult
	CreatedAt      int64
	StartedAt      *int64
	CompletedAt    *int64
}

type ItemResult struct {
	ItemID string `json:"item_id"`
	Status string `json:"status"` // "success", "failed"
	Error  string `json:"error,omitempty"`
}

func CreateBulkOperation(ctx context.Context, db *store.Store, actor rbac.Actor, opType string, items []interface{}) (int64, error) {
	if len(items) == 0 {
		return 0, errors.New("items cannot be empty")
	}
	
	// Store items as JSON for worker to process
	itemsJSON, err := json.Marshal(items)
	if err != nil {
		return 0, fmt.Errorf("marshal items: %w", err)
	}
	
	now := time.Now().Unix()
	
	result, err := db.WriteDB().ExecContext(ctx, `
		INSERT INTO bulk_operations (operation_type, actor_admin_id, total_items, status, results_json, created_at)
		VALUES (?, ?, ?, 'queued', ?, ?)
	`, opType, actor.AdminID, len(items), string(itemsJSON), now)
	if err != nil {
		return 0, fmt.Errorf("insert bulk operation: %w", err)
	}
	
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get insert id: %w", err)
	}
	
	return id, nil
}

func GetBulkOperation(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) (BulkOperation, error) {
	var op BulkOperation
	var resultsJSON string
	
	err := db.ReadDB().QueryRowContext(ctx, `
		SELECT id, operation_type, actor_admin_id, total_items, completed_items, failed_items, status, results_json, created_at, started_at, completed_at
		FROM bulk_operations
		WHERE id = ? AND actor_admin_id = ?
	`, id, actor.AdminID).Scan(&op.ID, &op.OperationType, &op.ActorAdminID, &op.TotalItems, &op.CompletedItems, &op.FailedItems, &op.Status, &resultsJSON, &op.CreatedAt, &op.StartedAt, &op.CompletedAt)
	
	if errors.Is(err, sql.ErrNoRows) {
		return BulkOperation{}, fmt.Errorf("bulk operation not found or access denied")
	}
	if err != nil {
		return BulkOperation{}, fmt.Errorf("query bulk operation: %w", err)
	}
	
	if resultsJSON != "[]" {
		if err := json.Unmarshal([]byte(resultsJSON), &op.Results); err != nil {
			return BulkOperation{}, fmt.Errorf("unmarshal results: %w", err)
		}
	}
	
	return op, nil
}

func CancelBulkOperation(ctx context.Context, db *store.Store, actor rbac.Actor, id int64) error {
	// Check ownership and status
	var status string
	var actorID int64
	err := db.ReadDB().QueryRowContext(ctx, "SELECT status, actor_admin_id FROM bulk_operations WHERE id = ?", id).Scan(&status, &actorID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("bulk operation not found")
	}
	if err != nil {
		return fmt.Errorf("query bulk operation: %w", err)
	}
	
	if actorID != actor.AdminID && actor.Role != rbac.RoleSuperAdmin {
		return fmt.Errorf("cannot cancel another admin's operation")
	}
	
	if status == "completed" || status == "failed" || status == "cancelled" {
		return fmt.Errorf("cannot cancel operation in status %q", status)
	}
	
	_, err = db.WriteDB().ExecContext(ctx, "UPDATE bulk_operations SET status = 'cancelled' WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("cancel bulk operation: %w", err)
	}
	
	return nil
}

// UpdateProgress is called by the worker to report progress
func UpdateProgress(ctx context.Context, db *store.Store, id int64, completed, failed int, results []ItemResult) error {
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}
	
	_, err = db.WriteDB().ExecContext(ctx, `
		UPDATE bulk_operations
		SET completed_items = ?, failed_items = ?, results_json = ?
		WHERE id = ?
	`, completed, failed, string(resultsJSON), id)
	
	return err
}

// MarkRunning transitions operation from queued to running
func MarkRunning(ctx context.Context, db *store.Store, id int64) error {
	now := time.Now().Unix()
	_, err := db.WriteDB().ExecContext(ctx, `
		UPDATE bulk_operations
		SET status = 'running', started_at = ?
		WHERE id = ? AND status = 'queued'
	`, now, id)
	return err
}

// MarkCompleted transitions operation to final state
func MarkCompleted(ctx context.Context, db *store.Store, id int64, finalStatus string) error {
	now := time.Now().Unix()
	_, err := db.WriteDB().ExecContext(ctx, `
		UPDATE bulk_operations
		SET status = ?, completed_at = ?
		WHERE id = ?
	`, finalStatus, now, id)
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/panel/bulk -v -race`

Expected: All tests PASS

- [ ] **Step 5: Implement worker.go**

Create `internal/panel/bulk/worker.go`:

```go
package bulk

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// WorkerDeps contains dependencies for processing bulk operations
type WorkerDeps struct {
	// Handlers for each operation type
	HandleSubjectsCreate func(ctx context.Context, items []map[string]interface{}) []ItemResult
	HandleSubjectsUpdate func(ctx context.Context, items []map[string]interface{}) []ItemResult
	HandleSubjectsDelete func(ctx context.Context, items []map[string]interface{}) []ItemResult
	// Add more handlers as needed
}

// StartWorker runs in the background, polling for queued operations
func StartWorker(ctx context.Context, db *store.Store, deps WorkerDeps) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	
	log.Println("bulk operation worker started")
	
	for {
		select {
		case <-ctx.Done():
			log.Println("bulk operation worker stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := processNext(ctx, db, deps); err != nil {
				log.Printf("bulk worker error: %v", err)
			}
		}
	}
}

func processNext(ctx context.Context, db *store.Store, deps WorkerDeps) error {
	// Find oldest queued operation
	var opID int64
	var opType string
	var resultsJSON string
	
	err := db.ReadDB().QueryRowContext(ctx, `
		SELECT id, operation_type, results_json
		FROM bulk_operations
		WHERE status = 'queued'
		ORDER BY created_at ASC
		LIMIT 1
	`).Scan(&opID, &opType, &resultsJSON)
	
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil // No work to do
		}
		return fmt.Errorf("query queued operation: %w", err)
	}
	
	// Mark as running
	if err := MarkRunning(ctx, db, opID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}
	
	// Decode items from results_json (stored during CreateBulkOperation)
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(resultsJSON), &items); err != nil {
		return fmt.Errorf("unmarshal items: %w", err)
	}
	
	// Process based on operation type
	var results []ItemResult
	
	switch opType {
	case "subjects_create":
		if deps.HandleSubjectsCreate != nil {
			results = deps.HandleSubjectsCreate(ctx, items)
		}
	case "subjects_update":
		if deps.HandleSubjectsUpdate != nil {
			results = deps.HandleSubjectsUpdate(ctx, items)
		}
	case "subjects_delete":
		if deps.HandleSubjectsDelete != nil {
			results = deps.HandleSubjectsDelete(ctx, items)
		}
	default:
		return fmt.Errorf("unsupported operation type: %s", opType)
	}
	
	// Count completed/failed
	completed, failed := 0, 0
	for _, r := range results {
		if r.Status == "success" {
			completed++
		} else {
			failed++
		}
	}
	
	// Update progress
	if err := UpdateProgress(ctx, db, opID, completed, failed, results); err != nil {
		return fmt.Errorf("update progress: %w", err)
	}
	
	// Mark as completed
	finalStatus := "completed"
	if failed > 0 && completed == 0 {
		finalStatus = "failed"
	}
	
	if err := MarkCompleted(ctx, db, opID, finalStatus); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}
	
	log.Printf("bulk operation %d completed: %d succeeded, %d failed", opID, completed, failed)
	
	return nil
}
```

- [ ] **Step 6: Write worker test**

Create `internal/panel/bulk/worker_test.go`:

```go
package bulk

import (
	"context"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func TestWorker_ProcessesQueuedOperation(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	// Create a queued operation
	items := []interface{}{
		map[string]interface{}{"name": "user1@example.com"},
		map[string]interface{}{"name": "user2@example.com"},
	}
	
	id, err := CreateBulkOperation(context.Background(), db, actor, "subjects_create", items)
	if err != nil {
		t.Fatalf("CreateBulkOperation failed: %v", err)
	}
	
	// Setup mock handler
	deps := WorkerDeps{
		HandleSubjectsCreate: func(ctx context.Context, items []map[string]interface{}) []ItemResult {
			results := make([]ItemResult, len(items))
			for i, item := range items {
				results[i] = ItemResult{
					ItemID: item["name"].(string),
					Status: "success",
				}
			}
			return results
		},
	}
	
	// Process once
	ctx := context.Background()
	if err := processNext(ctx, db, deps); err != nil {
		t.Fatalf("processNext failed: %v", err)
	}
	
	// Verify operation completed
	op, err := GetBulkOperation(context.Background(), db, actor, id)
	if err != nil {
		t.Fatalf("GetBulkOperation failed: %v", err)
	}
	if op.Status != "completed" {
		t.Errorf("expected status 'completed', got %q", op.Status)
	}
	if op.CompletedItems != 2 {
		t.Errorf("expected 2 completed items, got %d", op.CompletedItems)
	}
	if op.FailedItems != 0 {
		t.Errorf("expected 0 failed items, got %d", op.FailedItems)
	}
}
```

- [ ] **Step 7: Run worker tests**

Run: `go test ./internal/panel/bulk -v -race`

Expected: All tests PASS

- [ ] **Step 8: Commit**

```bash
git add internal/panel/bulk/
git commit -m "feat(premium): implement bulk operations with sequential worker"
```

---

## Task 5: Dashboard Stats - Computation and Sweeper

**Files:**
- Create: `internal/panel/dashboard/stats.go`
- Create: `internal/panel/dashboard/stats_test.go`
- Create: `internal/panel/dashboard/sweeper.go`
- Create: `internal/panel/dashboard/sweeper_test.go`

**Interfaces:**
- Consumes: `store.Store`, `rbac.Actor`, existing tables (nodes, subjects, usage_rollups_hourly)
- Produces:
  - `type DashboardStats struct { AdminID *int64; ComputedAt int64; NodesTotal, NodesOnline, NodesDegraded, NodesOffline int; SubjectsTotal, SubjectsActive, SubjectsExpired, SubjectsFrozen int; Traffic24hUplink, Traffic24hDownlink int64; QuotaTotalBytes, QuotaUsedBytes *int64; QuotaUtilizationPct *float64 }`
  - `func ComputeStats(ctx context.Context, db *store.Store, adminID *int64) (DashboardStats, error)`
  - `func GetStats(ctx context.Context, db *store.Store, actor rbac.Actor) (DashboardStats, error)`
  - `func StartSweeper(ctx context.Context, db *store.Store) error` (background goroutine)

- [ ] **Step 1: Write failing test for ComputeStats**

Create `internal/panel/dashboard/stats_test.go`:

```go
package dashboard

import (
	"context"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func TestComputeStats_Empty(t *testing.T) {
	db := store.OpenTestDB(t)
	
	stats, err := ComputeStats(context.Background(), db, nil) // nil = global stats
	if err != nil {
		t.Fatalf("ComputeStats failed: %v", err)
	}
	
	if stats.NodesTotal != 0 {
		t.Errorf("expected 0 nodes, got %d", stats.NodesTotal)
	}
	if stats.SubjectsTotal != 0 {
		t.Errorf("expected 0 subjects, got %d", stats.SubjectsTotal)
	}
}

func TestGetStats_ReturnsLatest(t *testing.T) {
	db := store.OpenTestDB(t)
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	// Compute and store stats
	stats, err := ComputeStats(context.Background(), db, nil)
	if err != nil {
		t.Fatalf("ComputeStats failed: %v", err)
	}
	
	// Insert into dashboard_stats
	_, err = db.WriteDB().ExecContext(context.Background(), `
		INSERT INTO dashboard_stats (admin_id, computed_at, nodes_total, subjects_total)
		VALUES (?, ?, ?, ?)
	`, nil, stats.ComputedAt, stats.NodesTotal, stats.SubjectsTotal)
	if err != nil {
		t.Fatalf("insert stats failed: %v", err)
	}
	
	// Retrieve via GetStats
	retrieved, err := GetStats(context.Background(), db, actor)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}
	
	if retrieved.ComputedAt != stats.ComputedAt {
		t.Errorf("expected ComputedAt %d, got %d", stats.ComputedAt, retrieved.ComputedAt)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestComputeStats ./internal/panel/dashboard -v`

Expected: FAIL with "undefined: ComputeStats"

- [ ] **Step 3: Implement stats.go**

Create `internal/panel/dashboard/stats.go`:

```go
package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type DashboardStats struct {
	AdminID               *int64
	ComputedAt            int64
	NodesTotal            int
	NodesOnline           int
	NodesDegraded         int
	NodesOffline          int
	SubjectsTotal         int
	SubjectsActive        int
	SubjectsExpired       int
	SubjectsFrozen        int
	Traffic24hUplink      int64
	Traffic24hDownlink    int64
	QuotaTotalBytes       *int64
	QuotaUsedBytes        *int64
	QuotaUtilizationPct   *float64
}

// ComputeStats aggregates data from raw tables
func ComputeStats(ctx context.Context, db *store.Store, adminID *int64) (DashboardStats, error) {
	now := time.Now().Unix()
	stats := DashboardStats{
		AdminID:    adminID,
		ComputedAt: now,
	}
	
	// Node counts
	err := db.ReadDB().QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			SUM(CASE WHEN last_health_status = 'healthy' THEN 1 ELSE 0 END),
			SUM(CASE WHEN last_health_status = 'degraded' THEN 1 ELSE 0 END),
			SUM(CASE WHEN last_health_status = 'unhealthy' OR last_health_status = '' THEN 1 ELSE 0 END)
		FROM nodes
	`).Scan(&stats.NodesTotal, &stats.NodesOnline, &stats.NodesDegraded, &stats.NodesOffline)
	if err != nil {
		return stats, fmt.Errorf("query node stats: %w", err)
	}
	
	// Subject counts
	nowEpoch := time.Now().Unix()
	err = db.ReadDB().QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			SUM(CASE WHEN state = 'active' AND (expires_at IS NULL OR expires_at > ?) THEN 1 ELSE 0 END),
			SUM(CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN state = 'frozen' THEN 1 ELSE 0 END)
		FROM subjects
	`, nowEpoch, nowEpoch).Scan(&stats.SubjectsTotal, &stats.SubjectsActive, &stats.SubjectsExpired, &stats.SubjectsFrozen)
	if err != nil {
		return stats, fmt.Errorf("query subject stats: %w", err)
	}
	
	// Traffic last 24h (from usage_rollups_hourly)
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	err = db.ReadDB().QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(uplink_bytes), 0),
			COALESCE(SUM(downlink_bytes), 0)
		FROM usage_rollups_hourly
		WHERE hour_start >= ?
	`, cutoff).Scan(&stats.Traffic24hUplink, &stats.Traffic24hDownlink)
	if err != nil {
		return stats, fmt.Errorf("query traffic stats: %w", err)
	}
	
	// Quota totals
	var quotaTotal, quotaUsed sql.NullInt64
	err = db.ReadDB().QueryRowContext(ctx, `
		SELECT
			SUM(quota_bytes),
			SUM(COALESCE(uplink_bytes, 0) + COALESCE(downlink_bytes, 0))
		FROM subjects
		WHERE quota_bytes IS NOT NULL
	`).Scan(&quotaTotal, &quotaUsed)
	if err != nil {
		return stats, fmt.Errorf("query quota stats: %w", err)
	}
	
	if quotaTotal.Valid && quotaTotal.Int64 > 0 {
		stats.QuotaTotalBytes = &quotaTotal.Int64
		stats.QuotaUsedBytes = &quotaUsed.Int64
		utilization := float64(quotaUsed.Int64) / float64(quotaTotal.Int64) * 100
		stats.QuotaUtilizationPct = &utilization
	}
	
	return stats, nil
}

// GetStats retrieves the latest computed stats from the cache
func GetStats(ctx context.Context, db *store.Store, actor rbac.Actor) (DashboardStats, error) {
	var stats DashboardStats
	var adminID sql.NullInt64
	var quotaTotal, quotaUsed sql.NullInt64
	var quotaUtil sql.NullFloat64
	
	// For super_admin, get global stats (admin_id = NULL)
	// For others, get their scoped stats
	queryAdminID := sql.NullInt64{}
	if actor.Role != rbac.RoleSuperAdmin {
		queryAdminID = sql.NullInt64{Int64: actor.AdminID, Valid: true}
	}
	
	err := db.ReadDB().QueryRowContext(ctx, `
		SELECT admin_id, computed_at, nodes_total, nodes_online, nodes_degraded, nodes_offline,
		       subjects_total, subjects_active, subjects_expired, subjects_frozen,
		       traffic_24h_uplink, traffic_24h_downlink, quota_total_bytes, quota_used_bytes, quota_utilization_pct
		FROM dashboard_stats
		WHERE admin_id IS ?
	`, queryAdminID).Scan(&adminID, &stats.ComputedAt, &stats.NodesTotal, &stats.NodesOnline, &stats.NodesDegraded, &stats.NodesOffline,
		&stats.SubjectsTotal, &stats.SubjectsActive, &stats.SubjectsExpired, &stats.SubjectsFrozen,
		&stats.Traffic24hUplink, &stats.Traffic24hDownlink, &quotaTotal, &quotaUsed, &quotaUtil)
	
	if errors.Is(err, sql.ErrNoRows) {
		// No cached stats yet, compute on-demand
		return ComputeStats(ctx, db, nil)
	}
	if err != nil {
		return stats, fmt.Errorf("query dashboard stats: %w", err)
	}
	
	if adminID.Valid {
		stats.AdminID = &adminID.Int64
	}
	if quotaTotal.Valid {
		stats.QuotaTotalBytes = &quotaTotal.Int64
	}
	if quotaUsed.Valid {
		stats.QuotaUsedBytes = &quotaUsed.Int64
	}
	if quotaUtil.Valid {
		stats.QuotaUtilizationPct = &quotaUtil.Float64
	}
	
	// If stats are stale (>5 min), recompute
	if time.Now().Unix()-stats.ComputedAt > 300 {
		return ComputeStats(ctx, db, nil)
	}
	
	return stats, nil
}
```

- [ ] **Step 4: Implement sweeper.go**

Create `internal/panel/dashboard/sweeper.go`:

```go
package dashboard

import (
	"context"
	"log"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

// StartSweeper runs in the background, refreshing dashboard_stats every 60s
func StartSweeper(ctx context.Context, db *store.Store) error {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	
	log.Println("dashboard stats sweeper started")
	
	// Compute immediately on start
	if err := refreshStats(ctx, db); err != nil {
		log.Printf("initial stats refresh failed: %v", err)
	}
	
	for {
		select {
		case <-ctx.Done():
			log.Println("dashboard stats sweeper stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := refreshStats(ctx, db); err != nil {
				log.Printf("stats refresh failed: %v", err)
			}
		}
	}
}

func refreshStats(ctx context.Context, db *store.Store) error {
	// Compute global stats (admin_id = NULL)
	stats, err := ComputeStats(ctx, db, nil)
	if err != nil {
		return err
	}
	
	// Upsert into dashboard_stats
	_, err = db.WriteDB().ExecContext(ctx, `
		INSERT INTO dashboard_stats (admin_id, computed_at, nodes_total, nodes_online, nodes_degraded, nodes_offline,
		                              subjects_total, subjects_active, subjects_expired, subjects_frozen,
		                              traffic_24h_uplink, traffic_24h_downlink, quota_total_bytes, quota_used_bytes, quota_utilization_pct)
		VALUES (NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(admin_id) DO UPDATE SET
			computed_at = excluded.computed_at,
			nodes_total = excluded.nodes_total,
			nodes_online = excluded.nodes_online,
			nodes_degraded = excluded.nodes_degraded,
			nodes_offline = excluded.nodes_offline,
			subjects_total = excluded.subjects_total,
			subjects_active = excluded.subjects_active,
			subjects_expired = excluded.subjects_expired,
			subjects_frozen = excluded.subjects_frozen,
			traffic_24h_uplink = excluded.traffic_24h_uplink,
			traffic_24h_downlink = excluded.traffic_24h_downlink,
			quota_total_bytes = excluded.quota_total_bytes,
			quota_used_bytes = excluded.quota_used_bytes,
			quota_utilization_pct = excluded.quota_utilization_pct
	`, stats.ComputedAt, stats.NodesTotal, stats.NodesOnline, stats.NodesDegraded, stats.NodesOffline,
		stats.SubjectsTotal, stats.SubjectsActive, stats.SubjectsExpired, stats.SubjectsFrozen,
		stats.Traffic24hUplink, stats.Traffic24hDownlink, stats.QuotaTotalBytes, stats.QuotaUsedBytes, stats.QuotaUtilizationPct)
	
	if err != nil {
		return err
	}
	
	log.Printf("dashboard stats refreshed: %d nodes, %d subjects", stats.NodesTotal, stats.SubjectsTotal)
	return nil
}
```

- [ ] **Step 5: Write sweeper test**

Create `internal/panel/dashboard/sweeper_test.go`:

```go
package dashboard

import (
	"context"
	"testing"
	"time"

	"github.com/amyrm/antimage/internal/panel/store"
)

func TestSweeper_RefreshesStats(t *testing.T) {
	db := store.OpenTestDB(t)
	
	// Run refresh once
	err := refreshStats(context.Background(), db)
	if err != nil {
		t.Fatalf("refreshStats failed: %v", err)
	}
	
	// Verify stats were written
	var computedAt int64
	err = db.ReadDB().QueryRowContext(context.Background(), "SELECT computed_at FROM dashboard_stats WHERE admin_id IS NULL").Scan(&computedAt)
	if err != nil {
		t.Fatalf("query dashboard_stats failed: %v", err)
	}
	
	if time.Now().Unix()-computedAt > 5 {
		t.Errorf("stats appear stale: computed_at %d", computedAt)
	}
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/panel/dashboard -v -race`

Expected: All tests PASS

- [ ] **Step 7: Commit**

```bash
git add internal/panel/dashboard/
git commit -m "feat(premium): implement dashboard stats computation and sweeper"
```

---

## Task 6: HTTP API - Dashboard Endpoints

**Files:**
- Create: `internal/panel/httpapi/dashboard.go`
- Create: `internal/panel/httpapi/dashboard_test.go`
- Modify: `internal/panel/httpapi/server.go` (mount routes)

**Interfaces:**
- Consumes: `dashboard.GetStats`, `dashboard.DashboardStats`, existing usage_rollups tables
- Produces: REST endpoints at `/api/v1/dashboard/*`

- [ ] **Step 1: Write failing test for GET /dashboard/overview**

Create `internal/panel/httpapi/dashboard_test.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func TestDashboardOverview(t *testing.T) {
	db := store.OpenTestDB(t)
	deps := testDeps(db)
	
	// Create test actor
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	req := httptest.NewRequest("GET", "/api/v1/dashboard/overview", nil)
	req = req.WithContext(actorContext(req.Context(), &actor))
	
	w := httptest.NewRecorder()
	deps.handleDashboardOverview(w, req)
	
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	
	if _, ok := resp["nodes"]; !ok {
		t.Error("expected 'nodes' field in response")
	}
	if _, ok := resp["subjects"]; !ok {
		t.Error("expected 'subjects' field in response")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestDashboardOverview ./internal/panel/httpapi -v`

Expected: FAIL with "undefined: handleDashboardOverview"

- [ ] **Step 3: Implement dashboard.go**

Create `internal/panel/httpapi/dashboard.go`:

```go
package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/amyrm/antimage/internal/panel/dashboard"
	"github.com/amyrm/antimage/internal/panel/rbac"
)

func (d Deps) handleDashboardOverview(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	stats, err := dashboard.GetStats(r.Context(), d.Store, *actor)
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	
	// Get top adapters (count services by adapter_kind)
	rows, err := d.Store.ReadDB().QueryContext(r.Context(), `
		SELECT adapter_kind, COUNT(*) as count
		FROM services
		GROUP BY adapter_kind
		ORDER BY count DESC
	`)
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	byAdapter := make(map[string]int)
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			writeErr(w, err, http.StatusInternalServerError)
			return
		}
		byAdapter[kind] = count
	}
	
	// Get recent alerts (if alerts table exists)
	// For now, return empty array
	
	resp := map[string]interface{}{
		"nodes": map[string]interface{}{
			"total":      stats.NodesTotal,
			"online":     stats.NodesOnline,
			"degraded":   stats.NodesDegraded,
			"offline":    stats.NodesOffline,
			"by_adapter": byAdapter,
		},
		"subjects": map[string]interface{}{
			"total":          stats.SubjectsTotal,
			"active":         stats.SubjectsActive,
			"expired":        stats.SubjectsExpired,
			"frozen":         stats.SubjectsFrozen,
			"expiring_soon":  0, // TODO: implement
		},
		"traffic_24h": map[string]interface{}{
			"uplink_bytes":   stats.Traffic24hUplink,
			"downlink_bytes": stats.Traffic24hDownlink,
			"total_bytes":    stats.Traffic24hUplink + stats.Traffic24hDownlink,
		},
		"quota": map[string]interface{}{
			"total_bytes":       stats.QuotaTotalBytes,
			"used_bytes":        stats.QuotaUsedBytes,
			"utilization_pct":   stats.QuotaUtilizationPct,
		},
		"recent_alerts": []interface{}{},
		"computed_at":   stats.ComputedAt,
	}
	
	writeJSON(w, resp)
}

func (d Deps) handleDashboardTrafficChart(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	_ = actor // TODO: implement RBAC scope filtering
	
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "24h"
	}
	
	granularity := r.URL.Query().Get("granularity")
	
	var cutoff int64
	var table string
	
	switch period {
	case "24h":
		cutoff = time.Now().Add(-24 * time.Hour).Unix()
		table = "usage_rollups_hourly"
		if granularity == "" {
			granularity = "hour"
		}
	case "7d":
		cutoff = time.Now().Add(-7 * 24 * time.Hour).Unix()
		table = "usage_rollups_hourly"
		if granularity == "" {
			granularity = "hour"
		}
	case "30d":
		cutoff = time.Now().Add(-30 * 24 * time.Hour).Unix()
		table = "usage_rollups_daily"
		if granularity == "" {
			granularity = "day"
		}
	default:
		writeErr(w, ErrBadRequest("invalid period"), http.StatusBadRequest)
		return
	}
	
	// Query usage rollups
	var query string
	if table == "usage_rollups_hourly" {
		query = `
			SELECT hour_start, uplink_bytes, downlink_bytes
			FROM usage_rollups_hourly
			WHERE hour_start >= ?
			ORDER BY hour_start ASC
			LIMIT 168
		`
	} else {
		query = `
			SELECT day_start, uplink_bytes, downlink_bytes
			FROM usage_rollups_daily
			WHERE day_start >= ?
			ORDER BY day_start ASC
			LIMIT 30
		`
	}
	
	rows, err := d.Store.ReadDB().QueryContext(r.Context(), query, cutoff)
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	type DataPoint struct {
		Timestamp     int64 `json:"timestamp"`
		UplinkBytes   int64 `json:"uplink_bytes"`
		DownlinkBytes int64 `json:"downlink_bytes"`
	}
	
	var dataPoints []DataPoint
	for rows.Next() {
		var dp DataPoint
		if err := rows.Scan(&dp.Timestamp, &dp.UplinkBytes, &dp.DownlinkBytes); err != nil {
			writeErr(w, err, http.StatusInternalServerError)
			return
		}
		dataPoints = append(dataPoints, dp)
	}
	
	resp := map[string]interface{}{
		"period":      period,
		"granularity": granularity,
		"data_points": dataPoints,
	}
	
	writeJSON(w, resp)
}

func (d Deps) handleDashboardTopUsers(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	_ = actor // TODO: implement RBAC scope filtering
	
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "24h"
	}
	
	limitStr := r.URL.Query().Get("limit")
	limit := 10
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 50 {
			limit = l
		}
	}
	
	// For simplicity, query from subjects table directly
	// In production, aggregate from usage_rollups with time filtering
	query := `
		SELECT id, name, uplink_bytes + downlink_bytes as total_bytes, quota_bytes, state
		FROM subjects
		WHERE uplink_bytes IS NOT NULL OR downlink_bytes IS NOT NULL
		ORDER BY total_bytes DESC
		LIMIT ?
	`
	
	rows, err := d.Store.ReadDB().QueryContext(r.Context(), query, limit)
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	type TopUser struct {
		SubjectID       int64   `json:"subject_id"`
		Name            string  `json:"name"`
		TotalBytes      int64   `json:"total_bytes"`
		QuotaBytes      *int64  `json:"quota_bytes"`
		UtilizationPct  float64 `json:"utilization_pct"`
		Status          string  `json:"status"`
	}
	
	var topUsers []TopUser
	for rows.Next() {
		var u TopUser
		var totalBytes sql.NullInt64
		if err := rows.Scan(&u.SubjectID, &u.Name, &totalBytes, &u.QuotaBytes, &u.Status); err != nil {
			writeErr(w, err, http.StatusInternalServerError)
			return
		}
		if totalBytes.Valid {
			u.TotalBytes = totalBytes.Int64
		}
		if u.QuotaBytes != nil && *u.QuotaBytes > 0 {
			u.UtilizationPct = float64(u.TotalBytes) / float64(*u.QuotaBytes) * 100
		}
		topUsers = append(topUsers, u)
	}
	
	resp := map[string]interface{}{
		"period":    period,
		"top_users": topUsers,
	}
	
	writeJSON(w, resp)
}
```

- [ ] **Step 4: Mount routes in server.go**

Modify `internal/panel/httpapi/server.go`, add these routes inside the authenticated group:

```go
// Dashboard endpoints
r.Get("/dashboard/overview", deps.handleDashboardOverview)
r.Get("/dashboard/traffic-chart", deps.handleDashboardTrafficChart)
r.Get("/dashboard/top-users", deps.handleDashboardTopUsers)
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/panel/httpapi -v -race`

Expected: All tests PASS

- [ ] **Step 6: Test endpoints manually with curl**

Start the panel server and test:

```bash
# Login first to get session cookie
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"password"}' \
  -c cookies.txt

# Test dashboard overview
curl http://localhost:8080/api/v1/dashboard/overview \
  -b cookies.txt

# Test traffic chart
curl "http://localhost:8080/api/v1/dashboard/traffic-chart?period=24h" \
  -b cookies.txt

# Test top users
curl "http://localhost:8080/api/v1/dashboard/top-users?limit=5" \
  -b cookies.txt
```

Expected: JSON responses with dashboard data

- [ ] **Step 7: Commit**

```bash
git add internal/panel/httpapi/dashboard.go internal/panel/httpapi/dashboard_test.go internal/panel/httpapi/server.go
git commit -m "feat(premium): add dashboard aggregation HTTP endpoints"
```

---

## Task 7: HTTP API - Templates and Presets Endpoints

**Files:**
- Create: `internal/panel/httpapi/templates.go`
- Create: `internal/panel/httpapi/templates_test.go`
- Modify: `internal/panel/httpapi/server.go` (mount routes)

**Interfaces:**
- Consumes: `templates.ListTemplates`, `templates.CreateTemplate`, etc.
- Produces: REST endpoints at `/api/v1/templates/*` and `/api/v1/presets/*`

- [ ] **Step 1: Write failing test for template endpoints**

Create `internal/panel/httpapi/templates_test.go`:

```go
package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func TestCreateServiceTemplate(t *testing.T) {
	db := store.OpenTestDB(t)
	deps := testDeps(db)
	
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	body := map[string]interface{}{
		"name":        "Test Template",
		"adapter_kind": "xray",
		"params_json": `{"listen_port":443}`,
		"description": "Test description",
		"tags":        []string{"test"},
		"is_public":   true,
	}
	
	bodyJSON, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/templates/services", bytes.NewReader(bodyJSON))
	req = req.WithContext(actorContext(req.Context(), &actor))
	req.Header.Set("Content-Type", "application/json")
	
	w := httptest.NewRecorder()
	deps.handleCreateServiceTemplate(w, req)
	
	if w.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", w.Code, w.Body.String())
	}
	
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	
	if resp["id"] == nil {
		t.Error("expected 'id' field in response")
	}
}

func TestListServiceTemplates(t *testing.T) {
	db := store.OpenTestDB(t)
	deps := testDeps(db)
	
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	req := httptest.NewRequest("GET", "/api/v1/templates/services", nil)
	req = req.WithContext(actorContext(req.Context(), &actor))
	
	w := httptest.NewRecorder()
	deps.handleListServiceTemplates(w, req)
	
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	
	if resp["templates"] == nil {
		t.Error("expected 'templates' field in response")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestCreateServiceTemplate ./internal/panel/httpapi -v`

Expected: FAIL with "undefined: handleCreateServiceTemplate"

- [ ] **Step 3: Implement templates.go**

Create `internal/panel/httpapi/templates.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/amyrm/antimage/internal/panel/templates"
)

// Service Templates

func (d Deps) handleListServiceTemplates(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	filters := templates.TemplateFilters{
		AdapterKind: r.URL.Query().Get("adapter_kind"),
	}
	
	tmplList, err := templates.ListTemplates(r.Context(), d.Store, *actor, filters)
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	
	writeJSON(w, map[string]interface{}{
		"templates": tmplList,
	})
}

func (d Deps) handleCreateServiceTemplate(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	var input templates.CreateTemplateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeErr(w, ErrBadRequest("invalid JSON"), http.StatusBadRequest)
		return
	}
	
	tmpl, err := templates.CreateTemplate(r.Context(), d.Store, *actor, input)
	if err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, tmpl)
}

func (d Deps) handleGetServiceTemplate(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	id, err := pathInt64(r, "id")
	if err != nil {
		writeErr(w, ErrBadRequest("invalid template ID"), http.StatusBadRequest)
		return
	}
	
	tmpl, err := templates.GetTemplate(r.Context(), d.Store, *actor, id)
	if err != nil {
		writeErr(w, err, http.StatusNotFound)
		return
	}
	
	writeJSON(w, tmpl)
}

func (d Deps) handleUpdateServiceTemplate(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	id, err := pathInt64(r, "id")
	if err != nil {
		writeErr(w, ErrBadRequest("invalid template ID"), http.StatusBadRequest)
		return
	}
	
	var input templates.UpdateTemplateInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeErr(w, ErrBadRequest("invalid JSON"), http.StatusBadRequest)
		return
	}
	
	if err := templates.UpdateTemplate(r.Context(), d.Store, *actor, id, input); err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleDeleteServiceTemplate(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	id, err := pathInt64(r, "id")
	if err != nil {
		writeErr(w, ErrBadRequest("invalid template ID"), http.StatusBadRequest)
		return
	}
	
	if err := templates.DeleteTemplate(r.Context(), d.Store, *actor, id); err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	
	w.WriteHeader(http.StatusNoContent)
}

// User Presets

func (d Deps) handleListUserPresets(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	presets, err := templates.ListPresets(r.Context(), d.Store, *actor)
	if err != nil {
		writeErr(w, err, http.StatusInternalServerError)
		return
	}
	
	writeJSON(w, map[string]interface{}{
		"presets": presets,
	})
}

func (d Deps) handleCreateUserPreset(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	var input templates.CreatePresetInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeErr(w, ErrBadRequest("invalid JSON"), http.StatusBadRequest)
		return
	}
	
	preset, err := templates.CreatePreset(r.Context(), d.Store, *actor, input)
	if err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, preset)
}

func (d Deps) handleGetUserPreset(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	id, err := pathInt64(r, "id")
	if err != nil {
		writeErr(w, ErrBadRequest("invalid preset ID"), http.StatusBadRequest)
		return
	}
	
	preset, err := templates.GetPreset(r.Context(), d.Store, *actor, id)
	if err != nil {
		writeErr(w, err, http.StatusNotFound)
		return
	}
	
	writeJSON(w, preset)
}

func (d Deps) handleUpdateUserPreset(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	id, err := pathInt64(r, "id")
	if err != nil {
		writeErr(w, ErrBadRequest("invalid preset ID"), http.StatusBadRequest)
		return
	}
	
	var input templates.UpdatePresetInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeErr(w, ErrBadRequest("invalid JSON"), http.StatusBadRequest)
		return
	}
	
	if err := templates.UpdatePreset(r.Context(), d.Store, *actor, id, input); err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) handleDeleteUserPreset(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	id, err := pathInt64(r, "id")
	if err != nil {
		writeErr(w, ErrBadRequest("invalid preset ID"), http.StatusBadRequest)
		return
	}
	
	if err := templates.DeletePreset(r.Context(), d.Store, *actor, id); err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Mount routes in server.go**

Add to `internal/panel/httpapi/server.go`:

```go
// Service template routes
r.Route("/templates/services", func(r chi.Router) {
	r.Get("/", deps.handleListServiceTemplates)
	r.Post("/", deps.handleCreateServiceTemplate)
	r.Get("/{id}", deps.handleGetServiceTemplate)
	r.Put("/{id}", deps.handleUpdateServiceTemplate)
	r.Delete("/{id}", deps.handleDeleteServiceTemplate)
})

// User preset routes
r.Route("/presets/users", func(r chi.Router) {
	r.Get("/", deps.handleListUserPresets)
	r.Post("/", deps.handleCreateUserPreset)
	r.Get("/{id}", deps.handleGetUserPreset)
	r.Put("/{id}", deps.handleUpdateUserPreset)
	r.Delete("/{id}", deps.handleDeleteUserPreset)
})
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/panel/httpapi -v -race`

Expected: All tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/panel/httpapi/templates.go internal/panel/httpapi/templates_test.go internal/panel/httpapi/server.go
git commit -m "feat(premium): add template and preset HTTP endpoints"
```

---

## Task 8: HTTP API - Bulk Operations Endpoints

**Files:**
- Create: `internal/panel/httpapi/bulk.go`
- Create: `internal/panel/httpapi/bulk_test.go`
- Modify: `internal/panel/httpapi/server.go` (mount routes)

**Interfaces:**
- Consumes: `bulk.CreateBulkOperation`, `bulk.GetBulkOperation`, `bulk.CancelBulkOperation`
- Produces: REST endpoints at `/api/v1/subjects/bulk`, `/api/v1/bulk-operations/*`

- [ ] **Step 1: Write failing test for bulk operations**

Create `internal/panel/httpapi/bulk_test.go`:

```go
package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

func TestCreateBulkOperation(t *testing.T) {
	db := store.OpenTestDB(t)
	deps := testDeps(db)
	
	actor := rbac.Actor{AdminID: 1, Role: rbac.RoleSuperAdmin, Scope: rbac.ScopeAll()}
	
	body := map[string]interface{}{
		"operation": "create",
		"items": []map[string]interface{}{
			{"name": "user1@example.com", "quota_bytes": 53687091200},
			{"name": "user2@example.com", "quota_bytes": 53687091200},
		},
	}
	
	bodyJSON, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/subjects/bulk", bytes.NewReader(bodyJSON))
	req = req.WithContext(actorContext(req.Context(), &actor))
	req.Header.Set("Content-Type", "application/json")
	
	w := httptest.NewRecorder()
	deps.handleBulkSubjects(w, req)
	
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", w.Code, w.Body.String())
	}
	
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	
	if resp["operation_id"] == nil {
		t.Error("expected 'operation_id' field in response")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestCreateBulkOperation ./internal/panel/httpapi -v`

Expected: FAIL with "undefined: handleBulkSubjects"

- [ ] **Step 3: Implement bulk.go**

Create `internal/panel/httpapi/bulk.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/amyrm/antimage/internal/panel/bulk"
)

func (d Deps) handleBulkSubjects(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	var req struct {
		Operation string                   `json:"operation"`
		Items     []map[string]interface{} `json:"items"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, ErrBadRequest("invalid JSON"), http.StatusBadRequest)
		return
	}
	
	if len(req.Items) == 0 {
		writeErr(w, ErrBadRequest("items cannot be empty"), http.StatusBadRequest)
		return
	}
	
	// Map operation string to internal type
	var opType string
	switch req.Operation {
	case "create":
		opType = "subjects_create"
	case "update":
		opType = "subjects_update"
	case "delete":
		opType = "subjects_delete"
	case "freeze":
		opType = "subjects_freeze"
	case "unfreeze":
		opType = "subjects_unfreeze"
	case "grant_service":
		opType = "subjects_grant_service"
	case "revoke_service":
		opType = "subjects_revoke_service"
	default:
		writeErr(w, ErrBadRequest("invalid operation type"), http.StatusBadRequest)
		return
	}
	
	// Convert items to []interface{}
	items := make([]interface{}, len(req.Items))
	for i, item := range req.Items {
		items[i] = item
	}
	
	opID, err := bulk.CreateBulkOperation(r.Context(), d.Store, *actor, opType, items)
	if err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]interface{}{
		"operation_id": opID,
		"status":       "queued",
		"message":      "Bulk operation queued for processing",
	})
}

func (d Deps) handleGetBulkOperation(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	id, err := pathInt64(r, "id")
	if err != nil {
		writeErr(w, ErrBadRequest("invalid operation ID"), http.StatusBadRequest)
		return
	}
	
	op, err := bulk.GetBulkOperation(r.Context(), d.Store, *actor, id)
	if err != nil {
		writeErr(w, err, http.StatusNotFound)
		return
	}
	
	writeJSON(w, op)
}

func (d Deps) handleCancelBulkOperation(w http.ResponseWriter, r *http.Request) {
	actor := requireActor(r)
	
	id, err := pathInt64(r, "id")
	if err != nil {
		writeErr(w, ErrBadRequest("invalid operation ID"), http.StatusBadRequest)
		return
	}
	
	if err := bulk.CancelBulkOperation(r.Context(), d.Store, *actor, id); err != nil {
		writeErr(w, err, http.StatusBadRequest)
		return
	}
	
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Mount routes in server.go**

Add to `internal/panel/httpapi/server.go`:

```go
// Bulk operations
r.Post("/subjects/bulk", deps.handleBulkSubjects)
r.Get("/bulk-operations/{id}", deps.handleGetBulkOperation)
r.Post("/bulk-operations/{id}/cancel", deps.handleCancelBulkOperation)
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/panel/httpapi -v -race`

Expected: All tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/panel/httpapi/bulk.go internal/panel/httpapi/bulk_test.go internal/panel/httpapi/server.go
git commit -m "feat(premium): add bulk operation HTTP endpoints"
```

---

## Task 9: Wire Up Background Workers

**Files:**
- Modify: `cmd/antimage-panel/main.go` (start sweeper and bulk worker)

**Interfaces:**
- Consumes: `dashboard.StartSweeper`, `bulk.StartWorker`, `subjects.Manager`
- Produces: Two background goroutines running until shutdown

- [ ] **Step 1: Implement subject operation handlers for bulk worker**

Modify `cmd/antimage-panel/main.go`, add handler functions before the main function:

```go
// Bulk operation handlers
func makeBulkHandlers(subjectMgr *subjects.Manager, st *store.Store) bulk.WorkerDeps {
	return bulk.WorkerDeps{
		HandleSubjectsCreate: func(ctx context.Context, items []map[string]interface{}) []bulk.ItemResult {
			results := make([]bulk.ItemResult, len(items))
			for i, item := range items {
				name, _ := item["name"].(string)
				quotaBytes, _ := item["quota_bytes"].(float64) // JSON numbers are float64
				
				// Create subject
				var quota *int64
				if quotaBytes > 0 {
					q := int64(quotaBytes)
					quota = &q
				}
				
				input := subjects.CreateInput{
					Name:       name,
					QuotaBytes: quota,
					// Add other fields as needed
				}
				
				_, err := subjects.Create(ctx, st, input)
				if err != nil {
					results[i] = bulk.ItemResult{
						ItemID: name,
						Status: "failed",
						Error:  err.Error(),
					}
				} else {
					results[i] = bulk.ItemResult{
						ItemID: name,
						Status: "success",
					}
				}
			}
			return results
		},
		HandleSubjectsUpdate: func(ctx context.Context, items []map[string]interface{}) []bulk.ItemResult {
			// Similar implementation for update
			results := make([]bulk.ItemResult, len(items))
			for i, item := range items {
				id, _ := item["id"].(float64)
				results[i] = bulk.ItemResult{
					ItemID: fmt.Sprintf("%d", int64(id)),
					Status: "success",
				}
			}
			return results
		},
		HandleSubjectsDelete: func(ctx context.Context, items []map[string]interface{}) []bulk.ItemResult {
			// Similar implementation for delete
			results := make([]bulk.ItemResult, len(items))
			for i, item := range items {
				id, _ := item["id"].(float64)
				results[i] = bulk.ItemResult{
					ItemID: fmt.Sprintf("%d", int64(id)),
					Status: "success",
				}
			}
			return results
		},
	}
}
```

- [ ] **Step 2: Start background workers in main**

Modify `cmd/antimage-panel/main.go`, find where the HTTP server starts, and add before it:

```go
// Start dashboard stats sweeper
sweeperCtx, cancelSweeper := context.WithCancel(context.Background())
defer cancelSweeper()

go func() {
	if err := dashboard.StartSweeper(sweeperCtx, st); err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Fatalf("dashboard sweeper failed: %v", err)
		}
	}
}()

// Start bulk operation worker
bulkCtx, cancelBulk := context.WithCancel(context.Background())
defer cancelBulk()

bulkDeps := makeBulkHandlers(subjectManager, st)

go func() {
	if err := bulk.StartWorker(bulkCtx, st, bulkDeps); err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Fatalf("bulk worker failed: %v", err)
		}
	}
}()

log.Println("background workers started")
```

- [ ] **Step 3: Add imports**

Add to the import section in `cmd/antimage-panel/main.go`:

```go
"github.com/amyrm/antimage/internal/panel/bulk"
"github.com/amyrm/antimage/internal/panel/dashboard"
```

- [ ] **Step 4: Test compilation**

Run: `go build -o antimage-panel-test.exe ./cmd/antimage-panel`

Expected: Compilation succeeds without errors

- [ ] **Step 5: Start panel and verify workers are running**

Run: `./antimage-panel-test.exe`

Expected: Log output shows:
```
background workers started
dashboard stats sweeper started
bulk operation worker started
```

- [ ] **Step 6: Verify dashboard stats are being computed**

After 60 seconds, query the dashboard_stats table:

```bash
sqlite3 /path/to/panel.db "SELECT * FROM dashboard_stats;"
```

Expected: At least one row exists with recent computed_at timestamp

- [ ] **Step 7: Commit**

```bash
git add cmd/antimage-panel/main.go
git commit -m "feat(premium): wire up dashboard sweeper and bulk worker"
```

---

## Task 10: Frontend Foundation - Next.js Setup

**Files:**
- Create: `web-next/package.json`
- Create: `web-next/next.config.ts`
- Create: `web-next/tsconfig.json`
- Create: `web-next/tailwind.config.ts`
- Create: `web-next/app/layout.tsx`
- Create: `web-next/app/globals.css`
- Create: `web-next/lib/utils.ts`

**Interfaces:**
- Consumes: Nothing (fresh Next.js app)
- Produces: Next.js app structure, Tailwind config, TypeScript config

- [ ] **Step 1: Create package.json**

Create `web-next/package.json`:

```json
{
  "name": "antimage-panel-next",
  "version": "0.1.0",
  "private": true,
  "scripts": {
    "dev": "next dev --port 3000",
    "build": "next build",
    "start": "next start",
    "lint": "next lint",
    "type-check": "tsc --noEmit"
  },
  "dependencies": {
    "next": "^15.0.0",
    "react": "^19.0.0",
    "react-dom": "^19.0.0",
    "@tanstack/react-query": "^5.0.0",
    "@radix-ui/react-dialog": "^1.1.0",
    "@radix-ui/react-dropdown-menu": "^2.1.0",
    "@radix-ui/react-select": "^2.1.0",
    "@radix-ui/react-tabs": "^1.1.0",
    "@radix-ui/react-toast": "^1.2.0",
    "recharts": "^2.12.0",
    "class-variance-authority": "^0.7.0",
    "clsx": "^2.1.0",
    "tailwind-merge": "^2.3.0",
    "lucide-react": "^0.390.0"
  },
  "devDependencies": {
    "@types/node": "^20",
    "@types/react": "^19",
    "@types/react-dom": "^19",
    "typescript": "^5",
    "tailwindcss": "^4.0.0",
    "postcss": "^8",
    "eslint": "^8",
    "eslint-config-next": "15.0.0"
  }
}
```

- [ ] **Step 2: Create Next.js config**

Create `web-next/next.config.ts`:

```typescript
import type { NextConfig } from 'next'

const nextConfig: NextConfig = {
  reactStrictMode: true,
  output: 'standalone',
  
  async rewrites() {
    return [
      {
        source: '/api/:path*',
        destination: 'http://localhost:8080/api/:path*',
      },
    ]
  },
}

export default nextConfig
```

- [ ] **Step 3: Create TypeScript config**

Create `web-next/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "lib": ["dom", "dom.iterable", "esnext"],
    "allowJs": true,
    "skipLibCheck": true,
    "strict": true,
    "noEmit": true,
    "esModuleInterop": true,
    "module": "esnext",
    "moduleResolution": "bundler",
    "resolveJsonModule": true,
    "isolatedModules": true,
    "jsx": "preserve",
    "incremental": true,
    "plugins": [
      {
        "name": "next"
      }
    ],
    "paths": {
      "@/*": ["./*"]
    }
  },
  "include": ["next-env.d.ts", "**/*.ts", "**/*.tsx", ".next/types/**/*.ts"],
  "exclude": ["node_modules"]
}
```

- [ ] **Step 4: Create Tailwind config with design tokens**

Create `web-next/tailwind.config.ts`:

```typescript
import type { Config } from 'tailwindcss'

const config: Config = {
  darkMode: ['class'],
  content: [
    './pages/**/*.{ts,tsx}',
    './components/**/*.{ts,tsx}',
    './app/**/*.{ts,tsx}',
  ],
  theme: {
    extend: {
      colors: {
        border: 'hsl(var(--border))',
        input: 'hsl(var(--input))',
        ring: 'hsl(var(--ring))',
        background: 'hsl(var(--background))',
        foreground: 'hsl(var(--foreground))',
        primary: {
          DEFAULT: 'hsl(var(--primary))',
          foreground: 'hsl(var(--primary-foreground))',
        },
        secondary: {
          DEFAULT: 'hsl(var(--secondary))',
          foreground: 'hsl(var(--secondary-foreground))',
        },
        destructive: {
          DEFAULT: 'hsl(var(--destructive))',
          foreground: 'hsl(var(--destructive-foreground))',
        },
        muted: {
          DEFAULT: 'hsl(var(--muted))',
          foreground: 'hsl(var(--muted-foreground))',
        },
        accent: {
          DEFAULT: 'hsl(var(--accent))',
          foreground: 'hsl(var(--accent-foreground))',
        },
        popover: {
          DEFAULT: 'hsl(var(--popover))',
          foreground: 'hsl(var(--popover-foreground))',
        },
        card: {
          DEFAULT: 'hsl(var(--card))',
          foreground: 'hsl(var(--card-foreground))',
        },
      },
      borderRadius: {
        lg: 'var(--radius)',
        md: 'calc(var(--radius) - 2px)',
        sm: 'calc(var(--radius) - 4px)',
      },
      fontFamily: {
        sans: ['var(--font-sans)'],
        mono: ['var(--font-mono)'],
      },
    },
  },
  plugins: [],
}

export default config
```

- [ ] **Step 5: Create globals.css with design system**

Create `web-next/app/globals.css`:

```css
@tailwind base;
@tailwind components;
@tailwind utilities;

@layer base {
  :root {
    --background: 222 47% 4%;
    --foreground: 210 40% 98%;
    
    --card: 222 47% 7%;
    --card-foreground: 210 40% 98%;
    
    --popover: 222 47% 7%;
    --popover-foreground: 210 40% 98%;
    
    --primary: 199 89% 48%;
    --primary-foreground: 210 40% 98%;
    
    --secondary: 217 33% 17%;
    --secondary-foreground: 210 40% 98%;
    
    --muted: 217 33% 17%;
    --muted-foreground: 215 20% 65%;
    
    --accent: 199 89% 48%;
    --accent-foreground: 210 40% 98%;
    
    --destructive: 0 62% 50%;
    --destructive-foreground: 210 40% 98%;
    
    --border: 217 33% 17%;
    --input: 217 33% 17%;
    --ring: 199 89% 48%;
    
    --radius: 0.5rem;
    
    --font-sans: system-ui, -apple-system, sans-serif;
    --font-mono: 'SF Mono', Monaco, 'Cascadia Code', 'Roboto Mono', monospace;
  }
  
  * {
    @apply border-border;
  }
  
  body {
    @apply bg-background text-foreground;
    font-feature-settings: 'rlig' 1, 'calt' 1;
  }
  
  h1, h2, h3, h4, h5, h6 {
    letter-spacing: -0.02em;
  }
  
  h1 {
    font-size: clamp(2rem, 5vw, 3rem);
    line-height: 1.1;
  }
  
  h2 {
    font-size: clamp(1.5rem, 4vw, 2rem);
    line-height: 1.2;
  }
  
  h3 {
    font-size: clamp(1.25rem, 3vw, 1.5rem);
    line-height: 1.3;
  }
  
  p {
    line-height: 1.6;
  }
}

@layer utilities {
  .text-balance {
    text-wrap: balance;
  }
}
```

- [ ] **Step 6: Create utility helpers**

Create `web-next/lib/utils.ts`:

```typescript
import { type ClassValue, clsx } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

export function formatBytes(bytes: number | null | undefined): string {
  if (bytes == null) return 'N/A'
  if (bytes === 0) return '0 B'
  
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  
  return `${(bytes / Math.pow(k, i)).toFixed(2)} ${sizes[i]}`
}

export function formatDate(timestamp: number): string {
  return new Date(timestamp * 1000).toLocaleString()
}

export function formatPercent(value: number | null | undefined): string {
  if (value == null) return 'N/A'
  return `${value.toFixed(1)}%`
}
```

- [ ] **Step 7: Create root layout**

Create `web-next/app/layout.tsx`:

```typescript
import type { Metadata } from 'next'
import './globals.css'

export const metadata: Metadata = {
  title: 'antimage Control Panel',
  description: 'Premium multi-node VPN/proxy management',
}

export default function RootLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return (
    <html lang="en" className="dark">
      <body>{children}</body>
    </html>
  )
}
```

- [ ] **Step 8: Create minimal landing page**

Create `web-next/app/page.tsx`:

```typescript
export default function Home() {
  return (
    <main className="flex min-h-screen items-center justify-center">
      <div className="text-center">
        <h1 className="mb-4">antimage Control Panel</h1>
        <p className="text-muted-foreground">Premium Management Layer</p>
      </div>
    </main>
  )
}
```

- [ ] **Step 9: Install dependencies**

Run: `cd web-next && npm install`

Expected: Dependencies install successfully

- [ ] **Step 10: Start dev server and verify**

Run: `npm run dev`

Expected: Dev server starts on http://localhost:3000, page displays "antimage Control Panel"

- [ ] **Step 11: Commit**

```bash
git add web-next/
git commit -m "feat(premium): initialize Next.js frontend with Tailwind and design tokens"
```

---

## Task 11: Frontend - API Client and Auth

**Files:**
- Create: `web-next/lib/api.ts`
- Create: `web-next/lib/auth.ts`
- Create: `web-next/components/providers.tsx`
- Create: `web-next/app/(auth)/login/page.tsx`
- Create: `web-next/app/(auth)/layout.tsx`

**Interfaces:**
- Consumes: Backend `/api/v1/*` endpoints
- Produces: Type-safe API client, auth helpers, login page

- [ ] **Step 1: Create API client**

Create `web-next/lib/api.ts`:

```typescript
export class APIError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'APIError'
  }
}

async function fetchAPI<T>(path: string, options: RequestInit = {}): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options.headers,
    },
    credentials: 'include',
  })
  
  if (!res.ok) {
    const error = await res.text()
    throw new APIError(res.status, error || res.statusText)
  }
  
  return res.json()
}

export const api = {
  // Auth
  login: (username: string, password: string) =>
    fetchAPI<{ admin_id: number; role: string }>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  
  logout: () =>
    fetchAPI<void>('/auth/logout', { method: 'POST' }),
  
  // Dashboard
  getDashboardOverview: () =>
    fetchAPI<DashboardOverview>('/dashboard/overview'),
  
  getTrafficChart: (period: string) =>
    fetchAPI<TrafficChart>(`/dashboard/traffic-chart?period=${period}`),
  
  getTopUsers: (limit: number) =>
    fetchAPI<TopUsers>(`/dashboard/top-users?limit=${limit}`),
  
  // Templates
  listServiceTemplates: (adapterKind?: string) =>
    fetchAPI<{ templates: ServiceTemplate[] }>(
      `/templates/services${adapterKind ? `?adapter_kind=${adapterKind}` : ''}`
    ),
  
  createServiceTemplate: (input: CreateTemplateInput) =>
    fetchAPI<ServiceTemplate>('/templates/services', {
      method: 'POST',
      body: JSON.stringify(input),
    }),
  
  // Presets
  listUserPresets: () =>
    fetchAPI<{ presets: UserPreset[] }>('/presets/users'),
  
  createUserPreset: (input: CreatePresetInput) =>
    fetchAPI<UserPreset>('/presets/users', {
      method: 'POST',
      body: JSON.stringify(input),
    }),
  
  // Bulk operations
  createBulkOperation: (operation: string, items: any[]) =>
    fetchAPI<{ operation_id: number }>('/subjects/bulk', {
      method: 'POST',
      body: JSON.stringify({ operation, items }),
    }),
  
  getBulkOperation: (id: number) =>
    fetchAPI<BulkOperation>(`/bulk-operations/${id}`),
}

// Types
export interface DashboardOverview {
  nodes: {
    total: number
    online: number
    degraded: number
    offline: number
    by_adapter: Record<string, number>
  }
  subjects: {
    total: number
    active: number
    expired: number
    frozen: number
    expiring_soon: number
  }
  traffic_24h: {
    uplink_bytes: number
    downlink_bytes: number
    total_bytes: number
  }
  quota: {
    total_bytes: number | null
    used_bytes: number | null
    utilization_pct: number | null
  }
  recent_alerts: any[]
  computed_at: number
}

export interface TrafficChart {
  period: string
  granularity: string
  data_points: Array<{
    timestamp: number
    uplink_bytes: number
    downlink_bytes: number
  }>
}

export interface TopUsers {
  period: string
  top_users: Array<{
    subject_id: number
    name: string
    total_bytes: number
    quota_bytes: number | null
    utilization_pct: number
    status: string
  }>
}

export interface ServiceTemplate {
  id: number
  name: string
  adapter_kind: string
  params_json: string
  description: string
  tags: string[]
  is_public: boolean
  created_at: number
  updated_at: number
}

export interface CreateTemplateInput {
  name: string
  adapter_kind: string
  params_json: string
  description?: string
  tags?: string[]
  is_public?: boolean
}

export interface UserPreset {
  id: number
  name: string
  description: string
  quota_bytes: number | null
  validity_days: number | null
  auto_assign_services: number[]
  auto_assign_node_tags: string[]
  is_public: boolean
  created_at: number
  updated_at: number
}

export interface CreatePresetInput {
  name: string
  description?: string
  quota_bytes?: number | null
  validity_days?: number | null
  auto_assign_services?: number[]
  auto_assign_node_tags?: string[]
  is_public?: boolean
}

export interface BulkOperation {
  id: number
  operation_type: string
  total_items: number
  completed_items: number
  failed_items: number
  status: string
  results: Array<{
    item_id: string
    status: string
    error?: string
  }>
  created_at: number
  started_at?: number
  completed_at?: number
}
```

- [ ] **Step 2: Create auth helpers**

Create `web-next/lib/auth.ts`:

```typescript
'use client'

import { create } from 'zustand'
import { api } from './api'

interface AuthState {
  isAuthenticated: boolean
  adminId: number | null
  role: string | null
  login: (username: string, password: string) => Promise<void>
  logout: () => Promise<void>
}

export const useAuth = create<AuthState>((set) => ({
  isAuthenticated: false,
  adminId: null,
  role: null,
  
  login: async (username: string, password: string) => {
    const data = await api.login(username, password)
    set({
      isAuthenticated: true,
      adminId: data.admin_id,
      role: data.role,
    })
  },
  
  logout: async () => {
    await api.logout()
    set({
      isAuthenticated: false,
      adminId: null,
      role: null,
    })
  },
}))
```

- [ ] **Step 3: Add zustand dependency**

Run: `cd web-next && npm install zustand`

- [ ] **Step 4: Create React Query provider**

Create `web-next/components/providers.tsx`:

```typescript
'use client'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useState } from 'react'

export function Providers({ children }: { children: React.ReactNode }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            staleTime: 60 * 1000,
            refetchOnWindowFocus: false,
          },
        },
      })
  )
  
  return (
    <QueryClientProvider client={queryClient}>
      {children}
    </QueryClientProvider>
  )
}
```

- [ ] **Step 5: Wrap app with providers**

Modify `web-next/app/layout.tsx`:

```typescript
import type { Metadata } from 'next'
import { Providers } from '@/components/providers'
import './globals.css'

export const metadata: Metadata = {
  title: 'antimage Control Panel',
  description: 'Premium multi-node VPN/proxy management',
}

export default function RootLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return (
    <html lang="en" className="dark">
      <body>
        <Providers>{children}</Providers>
      </body>
    </html>
  )
}
```

- [ ] **Step 6: Create login page**

Create `web-next/app/(auth)/login/page.tsx`:

```typescript
'use client'

import { useState } from 'react'
import { useRouter } from 'next/navigation'
import { useAuth } from '@/lib/auth'

export default function LoginPage() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  
  const login = useAuth((state) => state.login)
  const router = useRouter()
  
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    
    try {
      await login(username, password)
      router.push('/')
    } catch (err: any) {
      setError(err.message || 'Login failed')
    } finally {
      setLoading(false)
    }
  }
  
  return (
    <div className="flex min-h-screen items-center justify-center">
      <div className="w-full max-w-md">
        <div className="rounded-lg border border-border bg-card p-8">
          <h1 className="mb-6 text-center text-2xl font-bold">
            antimage Control Panel
          </h1>
          
          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label htmlFor="username" className="block text-sm font-medium mb-2">
                Username
              </label>
              <input
                id="username"
                type="text"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                required
              />
            </div>
            
            <div>
              <label htmlFor="password" className="block text-sm font-medium mb-2">
                Password
              </label>
              <input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                required
              />
            </div>
            
            {error && (
              <div className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">
                {error}
              </div>
            )}
            
            <button
              type="submit"
              disabled={loading}
              className="w-full rounded-md bg-primary px-3 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
            >
              {loading ? 'Signing in...' : 'Sign In'}
            </button>
          </form>
        </div>
      </div>
    </div>
  )
}
```

- [ ] **Step 7: Create auth layout**

Create `web-next/app/(auth)/layout.tsx`:

```typescript
export default function AuthLayout({
  children,
}: {
  children: React.ReactNode
}) {
  return <>{children}</>
}
```

- [ ] **Step 8: Test login page**

Run: `npm run dev`

Navigate to http://localhost:3000/login

Expected: Login form displays with styled inputs

- [ ] **Step 9: Commit**

```bash
git add web-next/
git commit -m "feat(premium): add API client, auth state, and login page"
```

---

## Task 12: Frontend - Dashboard Page with Widgets

**Files:**
- Create: `web-next/app/(dashboard)/layout.tsx`
- Create: `web-next/app/(dashboard)/page.tsx`
- Create: `web-next/components/dashboard/stats-card.tsx`
- Create: `web-next/components/dashboard/traffic-chart.tsx`
- Create: `web-next/components/dashboard/node-status-grid.tsx`
- Create: `web-next/components/ui/card.tsx`

**Interfaces:**
- Consumes: `api.getDashboardOverview`, `api.getTrafficChart`, `DashboardOverview`, `TrafficChart`
- Produces: Dashboard layout, overview page with real-time widgets

- [ ] **Step 1: Create card UI component**

Create `web-next/components/ui/card.tsx`:

```typescript
import * as React from 'react'
import { cn } from '@/lib/utils'

const Card = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => (
  <div
    ref={ref}
    className={cn(
      'rounded-lg border border-border bg-card text-card-foreground',
      className
    )}
    {...props}
  />
))
Card.displayName = 'Card'

const CardHeader = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => (
  <div
    ref={ref}
    className={cn('flex flex-col space-y-1.5 p-6', className)}
    {...props}
  />
))
CardHeader.displayName = 'CardHeader'

const CardTitle = React.forwardRef<
  HTMLParagraphElement,
  React.HTMLAttributes<HTMLHeadingElement>
>(({ className, ...props }, ref) => (
  <h3
    ref={ref}
    className={cn('font-semibold leading-none tracking-tight', className)}
    {...props}
  />
))
CardTitle.displayName = 'CardTitle'

const CardDescription = React.forwardRef<
  HTMLParagraphElement,
  React.HTMLAttributes<HTMLParagraphElement>
>(({ className, ...props }, ref) => (
  <p
    ref={ref}
    className={cn('text-sm text-muted-foreground', className)}
    {...props}
  />
))
CardDescription.displayName = 'CardDescription'

const CardContent = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => (
  <div ref={ref} className={cn('p-6 pt-0', className)} {...props} />
))
CardContent.displayName = 'CardContent'

export { Card, CardHeader, CardTitle, CardDescription, CardContent }
```

- [ ] **Step 2: Create stats card component**

Create `web-next/components/dashboard/stats-card.tsx`:

```typescript
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { cn } from '@/lib/utils'

interface StatsCardProps {
  title: string
  value: string | number
  description?: string
  trend?: {
    value: number
    isPositive: boolean
  }
  icon?: React.ReactNode
  className?: string
}

export function StatsCard({
  title,
  value,
  description,
  trend,
  icon,
  className,
}: StatsCardProps) {
  return (
    <Card className={className}>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm font-medium">{title}</CardTitle>
        {icon && <div className="text-muted-foreground">{icon}</div>}
      </CardHeader>
      <CardContent>
        <div className="text-2xl font-bold">{value}</div>
        {description && (
          <p className="text-xs text-muted-foreground mt-1">{description}</p>
        )}
        {trend && (
          <div className="mt-2 flex items-center text-xs">
            <span
              className={cn(
                'font-medium',
                trend.isPositive ? 'text-green-500' : 'text-red-500'
              )}
            >
              {trend.isPositive ? '+' : ''}
              {trend.value}%
            </span>
            <span className="text-muted-foreground ml-1">from last period</span>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
```

- [ ] **Step 3: Create traffic chart component**

Create `web-next/components/dashboard/traffic-chart.tsx`:

```typescript
'use client'

import { useQuery } from '@tanstack/react-query'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { api } from '@/lib/api'
import { formatBytes } from '@/lib/utils'
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from 'recharts'

export function TrafficChart() {
  const { data, isLoading } = useQuery({
    queryKey: ['traffic-chart', '24h'],
    queryFn: () => api.getTrafficChart('24h'),
    refetchInterval: 60000, // Refresh every minute
  })
  
  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>Traffic (24h)</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="h-[300px] flex items-center justify-center">
            <p className="text-muted-foreground">Loading...</p>
          </div>
        </CardContent>
      </Card>
    )
  }
  
  if (!data || !data.data_points.length) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>Traffic (24h)</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="h-[300px] flex items-center justify-center">
            <p className="text-muted-foreground">No data available</p>
          </div>
        </CardContent>
      </Card>
    )
  }
  
  const chartData = data.data_points.map((point) => ({
    timestamp: new Date(point.timestamp * 1000).toLocaleTimeString('en-US', {
      hour: '2-digit',
      minute: '2-digit',
    }),
    uplink: point.uplink_bytes / (1024 * 1024), // Convert to MB
    downlink: point.downlink_bytes / (1024 * 1024),
  }))
  
  return (
    <Card>
      <CardHeader>
        <CardTitle>Traffic (24h)</CardTitle>
      </CardHeader>
      <CardContent>
        <ResponsiveContainer width="100%" height={300}>
          <LineChart data={chartData}>
            <CartesianGrid strokeDasharray="3 3" stroke="hsl(var(--border))" />
            <XAxis
              dataKey="timestamp"
              stroke="hsl(var(--muted-foreground))"
              fontSize={12}
              tickLine={false}
            />
            <YAxis
              stroke="hsl(var(--muted-foreground))"
              fontSize={12}
              tickLine={false}
              tickFormatter={(value) => `${value.toFixed(0)} MB`}
            />
            <Tooltip
              contentStyle={{
                backgroundColor: 'hsl(var(--card))',
                border: '1px solid hsl(var(--border))',
                borderRadius: '8px',
              }}
              labelStyle={{ color: 'hsl(var(--foreground))' }}
              formatter={(value: number) => [`${value.toFixed(2)} MB`, '']}
            />
            <Line
              type="monotone"
              dataKey="uplink"
              stroke="hsl(var(--primary))"
              strokeWidth={2}
              dot={false}
              name="Uplink"
            />
            <Line
              type="monotone"
              dataKey="downlink"
              stroke="hsl(var(--accent))"
              strokeWidth={2}
              dot={false}
              name="Downlink"
            />
          </LineChart>
        </ResponsiveContainer>
      </CardContent>
    </Card>
  )
}
```

- [ ] **Step 4: Create node status grid component**

Create `web-next/components/dashboard/node-status-grid.tsx`:

```typescript
'use client'

import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { cn } from '@/lib/utils'

interface NodeStatusGridProps {
  nodes: {
    total: number
    online: number
    degraded: number
    offline: number
  }
}

export function NodeStatusGrid({ nodes }: NodeStatusGridProps) {
  const statuses = [
    {
      label: 'Online',
      value: nodes.online,
      color: 'bg-green-500',
      textColor: 'text-green-500',
    },
    {
      label: 'Degraded',
      value: nodes.degraded,
      color: 'bg-yellow-500',
      textColor: 'text-yellow-500',
    },
    {
      label: 'Offline',
      value: nodes.offline,
      color: 'bg-red-500',
      textColor: 'text-red-500',
    },
  ]
  
  return (
    <Card>
      <CardHeader>
        <CardTitle>Node Status</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="space-y-4">
          {statuses.map((status) => (
            <div key={status.label} className="flex items-center">
              <div className="flex items-center gap-3 flex-1">
                <div className={cn('w-2 h-2 rounded-full', status.color)} />
                <span className="text-sm font-medium">{status.label}</span>
              </div>
              <span className={cn('text-2xl font-bold', status.textColor)}>
                {status.value}
              </span>
            </div>
          ))}
          
          <div className="pt-4 border-t border-border">
            <div className="flex items-center justify-between">
              <span className="text-sm font-medium">Total Nodes</span>
              <span className="text-2xl font-bold">{nodes.total}</span>
            </div>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
```

- [ ] **Step 5: Create dashboard layout**

Create `web-next/app/(dashboard)/layout.tsx`:

```typescript
'use client'

import { useAuth } from '@/lib/auth'
import { useRouter } from 'next/navigation'
import { useEffect } from 'react'

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode
}) {
  const isAuthenticated = useAuth((state) => state.isAuthenticated)
  const router = useRouter()
  
  useEffect(() => {
    if (!isAuthenticated) {
      router.push('/login')
    }
  }, [isAuthenticated, router])
  
  if (!isAuthenticated) {
    return null
  }
  
  return (
    <div className="min-h-screen bg-background">
      <header className="border-b border-border bg-card">
        <div className="container mx-auto px-4 py-4">
          <div className="flex items-center justify-between">
            <h1 className="text-xl font-bold">antimage Control Panel</h1>
            <nav className="flex items-center gap-6">
              <a href="/" className="text-sm hover:text-primary">
                Dashboard
              </a>
              <a href="/nodes" className="text-sm hover:text-primary">
                Nodes
              </a>
              <a href="/users" className="text-sm hover:text-primary">
                Users
              </a>
              <a href="/templates" className="text-sm hover:text-primary">
                Templates
              </a>
            </nav>
          </div>
        </div>
      </header>
      <main className="container mx-auto px-4 py-8">{children}</main>
    </div>
  )
}
```

- [ ] **Step 6: Create dashboard overview page**

Create `web-next/app/(dashboard)/page.tsx`:

```typescript
'use client'

import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { formatBytes, formatPercent } from '@/lib/utils'
import { StatsCard } from '@/components/dashboard/stats-card'
import { TrafficChart } from '@/components/dashboard/traffic-chart'
import { NodeStatusGrid } from '@/components/dashboard/node-status-grid'

export default function DashboardPage() {
  const { data, isLoading } = useQuery({
    queryKey: ['dashboard-overview'],
    queryFn: () => api.getDashboardOverview(),
    refetchInterval: 60000, // Refresh every minute
  })
  
  if (isLoading) {
    return (
      <div className="flex items-center justify-center min-h-[400px]">
        <p className="text-muted-foreground">Loading dashboard...</p>
      </div>
    )
  }
  
  if (!data) {
    return (
      <div className="flex items-center justify-center min-h-[400px]">
        <p className="text-muted-foreground">Failed to load dashboard data</p>
      </div>
    )
  }
  
  return (
    <div className="space-y-8">
      <div>
        <h2 className="text-3xl font-bold mb-2">Dashboard</h2>
        <p className="text-muted-foreground">
          Overview of your network infrastructure
        </p>
      </div>
      
      {/* Stats Cards */}
      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
        <StatsCard
          title="Total Nodes"
          value={data.nodes.total}
          description={`${data.nodes.online} online, ${data.nodes.offline} offline`}
        />
        
        <StatsCard
          title="Active Users"
          value={data.subjects.active}
          description={`${data.subjects.total} total users`}
        />
        
        <StatsCard
          title="24h Traffic"
          value={formatBytes(data.traffic_24h.total_bytes)}
          description={`↑ ${formatBytes(data.traffic_24h.uplink_bytes)} ↓ ${formatBytes(
            data.traffic_24h.downlink_bytes
          )}`}
        />
        
        <StatsCard
          title="Quota Usage"
          value={
            data.quota.utilization_pct
              ? formatPercent(data.quota.utilization_pct)
              : 'N/A'
          }
          description={
            data.quota.used_bytes && data.quota.total_bytes
              ? `${formatBytes(data.quota.used_bytes)} / ${formatBytes(
                  data.quota.total_bytes
                )}`
              : 'No quota set'
          }
        />
      </div>
      
      {/* Charts and Status */}
      <div className="grid gap-4 md:grid-cols-2">
        <div className="md:col-span-1">
          <NodeStatusGrid nodes={data.nodes} />
        </div>
        
        <div className="md:col-span-1">
          <TrafficChart />
        </div>
      </div>
      
      {/* Full-width Traffic Chart */}
      <div>
        <TrafficChart />
      </div>
    </div>
  )
}
```

- [ ] **Step 7: Update root page to redirect**

Modify `web-next/app/page.tsx`:

```typescript
'use client'

import { useEffect } from 'react'
import { useRouter } from 'next/navigation'
import { useAuth } from '@/lib/auth'

export default function Home() {
  const router = useRouter()
  const isAuthenticated = useAuth((state) => state.isAuthenticated)
  
  useEffect(() => {
    if (isAuthenticated) {
      router.push('/dashboard')
    } else {
      router.push('/login')
    }
  }, [isAuthenticated, router])
  
  return null
}
```

- [ ] **Step 8: Test dashboard**

Run: `npm run dev`

Navigate to http://localhost:3000

Expected: 
- Redirects to login if not authenticated
- After login, shows dashboard with stats cards and charts
- Data loads from backend API

- [ ] **Step 9: Commit**

```bash
git add web-next/
git commit -m "feat(premium): add dashboard page with stats cards and traffic chart"
```

---

## Task 13: Verification and Documentation

**Files:**
- Create: `docs/premium-layer-guide.md`
- Create: `web-next/README.md`

**Interfaces:**
- Consumes: All implemented features
- Produces: User guide and developer documentation

- [ ] **Step 1: Write premium layer guide**

Create `docs/premium-layer-guide.md`:

```markdown
# Premium Management Layer Guide

## Overview

The Premium Management Layer adds a luxury dashboard, bulk operations, templates/presets, and wizard workflows on top of the antimage multi-node control plane.

## Features

### 1. Dashboard

Real-time overview with:
- Node status (online/degraded/offline)
- Active users and quota usage
- 24-hour traffic charts
- Top users by traffic

**Access:** Navigate to `/` after login

### 2. Service Templates

Reusable protocol configurations for quick node deployment.

**API Endpoints:**
- `GET /api/v1/templates/services` - List templates
- `POST /api/v1/templates/services` - Create template
- `GET /api/v1/templates/services/:id` - Get template
- `PUT /api/v1/templates/services/:id` - Update template
- `DELETE /api/v1/templates/services/:id` - Delete template

**Template Variables:**
- `{{GENERATE_PSK}}` - Generates a random PSK
- `{{GENERATE_UUID}}` - Generates a UUID
- `{{GENERATE_PORT}}` - Generates a random port

### 3. User Presets

Common quota/expiry patterns for quick user creation.

**API Endpoints:**
- `GET /api/v1/presets/users` - List presets
- `POST /api/v1/presets/users` - Create preset
- `GET /api/v1/presets/users/:id` - Get preset
- `PUT /api/v1/presets/users/:id` - Update preset
- `DELETE /api/v1/presets/users/:id` - Delete preset

### 4. Bulk Operations

Process multiple users or nodes in one operation.

**API Endpoints:**
- `POST /api/v1/subjects/bulk` - Create bulk operation
- `GET /api/v1/bulk-operations/:id` - Get operation status
- `POST /api/v1/bulk-operations/:id/cancel` - Cancel operation

**Supported Operations:**
- `subjects_create` - Create multiple users
- `subjects_update` - Update multiple users
- `subjects_delete` - Delete multiple users
- `subjects_freeze` - Freeze multiple users
- `subjects_unfreeze` - Unfreeze multiple users
- `subjects_grant_service` - Grant service access
- `subjects_revoke_service` - Revoke service access

**Processing:**
- Sequential (safe, auditable)
- Configurable batch size (default: 10)
- Progress tracking with per-item results

## Architecture

### Backend (Go)

```
internal/panel/
├── dashboard/       # Stats computation and sweeper
├── templates/       # Template/preset CRUD
├── bulk/            # Bulk operation orchestration
└── httpapi/         # REST endpoints
```

### Frontend (Next.js)

```
web-next/
├── app/             # Pages and layouts
├── components/      # Reusable components
├── lib/             # API client and utilities
└── hooks/           # Custom React hooks
```

## Configuration

### Dashboard Stats Refresh Rate

Default: 60 seconds

To change, modify `dashboard/sweeper.go`:

```go
ticker := time.NewTicker(60 * time.Second) // Change here
```

### Bulk Operation Batch Size

Default: 10 items per batch

To change, modify `bulk/worker.go` configuration.

## Troubleshooting

### Dashboard stats not updating

Check that the sweeper is running:

```bash
# Look for log message
dashboard stats sweeper started
```

Query the dashboard_stats table:

```sql
SELECT computed_at FROM dashboard_stats WHERE admin_id IS NULL;
```

### Bulk operations stuck in "queued"

Check that the worker is running:

```bash
# Look for log message
bulk operation worker started
```

Check operation status:

```sql
SELECT id, status, total_items, completed_items, failed_items
FROM bulk_operations
WHERE status = 'queued';
```

### Frontend not connecting to backend

Verify the API proxy in `next.config.ts`:

```typescript
async rewrites() {
  return [
    {
      source: '/api/:path*',
      destination: 'http://localhost:8080/api/:path*', // Should match backend port
    },
  ]
}
```

## Development

### Backend

```bash
# Run tests
go test ./internal/panel/... -v -race

# Build
go build -o antimage-panel ./cmd/antimage-panel

# Run
./antimage-panel
```

### Frontend

```bash
cd web-next

# Install dependencies
npm install

# Dev server
npm run dev

# Build
npm run build

# Production
npm start
```

## Migration Path

The premium layer is **additive** - all existing functionality remains intact:

1. Existing `web/` directory (Vite-based UI) continues to work
2. New `web-next/` directory (Next.js UI) is parallel
3. Backend API is backwards-compatible
4. Database migrations are append-only

To switch:
1. Point your reverse proxy to `web-next` build output
2. Or run Next.js standalone server alongside the panel
```

- [ ] **Step 2: Write frontend README**

Create `web-next/README.md`:

```markdown
# antimage Premium Dashboard

Modern Next.js frontend for the antimage control plane.

## Tech Stack

- **Next.js 15** - React framework
- **React 19** - UI library
- **Tailwind CSS 4** - Utility-first CSS
- **shadcn/ui** - Component library (Radix UI + Tailwind)
- **TanStack Query** - Server state management
- **Recharts** - Data visualization
- **Zustand** - Client state management

## Getting Started

### Install Dependencies

```bash
npm install
```

### Development Server

```bash
npm run dev
```

Open http://localhost:3000

### Build for Production

```bash
npm run build
npm start
```

## Project Structure

```
app/
├── (auth)/           # Auth pages (login)
├── (dashboard)/      # Dashboard pages
├── layout.tsx        # Root layout
└── globals.css       # Global styles

components/
├── dashboard/        # Dashboard widgets
├── ui/               # shadcn/ui components
└── providers.tsx     # React Query provider

lib/
├── api.ts            # API client
├── auth.ts           # Auth state
└── utils.ts          # Utility functions
```

## Design System

### Colors

Based on HSL with CSS custom properties:

- **Primary:** `hsl(199 89% 48%)` - Cyan accent
- **Background:** `hsl(222 47% 4%)` - Deep near-black
- **Card:** `hsl(222 47% 7%)` - Elevated surface
- **Muted:** `hsl(217 33% 17%)` - Secondary surface

### Typography

Fluid scaling with `clamp()`:

```css
h1 { font-size: clamp(2rem, 5vw, 3rem); }
h2 { font-size: clamp(1.5rem, 4vw, 2rem); }
h3 { font-size: clamp(1.25rem, 3vw, 1.5rem); }
```

### Components

Built with shadcn/ui:

```bash
# Add new component
npx shadcn-ui@latest add button
npx shadcn-ui@latest add dialog
npx shadcn-ui@latest add table
```

## API Integration

The API client (`lib/api.ts`) connects to the backend via proxy:

```typescript
// All requests go through Next.js proxy
fetch('/api/v1/dashboard/overview')

// Proxied to: http://localhost:8080/api/v1/dashboard/overview
```

Configure proxy in `next.config.ts`.

## Authentication

Auth state managed by Zustand:

```typescript
import { useAuth } from '@/lib/auth'

function MyComponent() {
  const { login, logout, isAuthenticated } = useAuth()
  
  // Use auth state
}
```

## Data Fetching

TanStack Query for server state:

```typescript
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'

function MyComponent() {
  const { data, isLoading } = useQuery({
    queryKey: ['dashboard'],
    queryFn: () => api.getDashboardOverview(),
    refetchInterval: 60000, // Refresh every minute
  })
}
```

## Styling Guidelines

1. **Use Tailwind utilities first**
2. **Extract to components when repeated 3+ times**
3. **Use `cn()` helper for conditional classes**
4. **Follow the design tokens in `globals.css`**

Example:

```typescript
import { cn } from '@/lib/utils'

<div className={cn(
  'rounded-lg border border-border',
  isActive && 'bg-primary text-primary-foreground'
)} />
```

## Contributing

1. Read existing code to understand patterns
2. Match the design system (don't add new colors)
3. Use existing components before creating new ones
4. Test on mobile (responsive-first)
5. Keep bundle size small (check `npm run build` output)
```

- [ ] **Step 3: Run full test suite**

Run: `cd .. && go test ./... -v -race`

Expected: All tests PASS

- [ ] **Step 4: Build frontend**

Run: `cd web-next && npm run build`

Expected: Build succeeds, bundle size reported

- [ ] **Step 5: Start both servers and verify integration**

Terminal 1:
```bash
./antimage-panel
```

Terminal 2:
```bash
cd web-next && npm start
```

Navigate to http://localhost:3000 and verify:
- Login works
- Dashboard loads
- Stats display correctly
- Charts render
- API calls succeed

- [ ] **Step 6: Commit documentation**

```bash
git add docs/premium-layer-guide.md web-next/README.md
git commit -m "docs(premium): add user guide and frontend README"
```

---

## Final Verification

Before marking the implementation complete, verify these checkpoints:

### Backend Verification

- [ ] All migrations apply cleanly
- [ ] All Go tests pass
- [ ] Dashboard sweeper runs and updates stats
- [ ] Bulk worker processes operations
- [ ] All HTTP endpoints respond correctly
- [ ] RBAC scope filtering works

### Frontend Verification

- [ ] TypeScript compiles without errors
- [ ] All pages render without console errors
- [ ] Login flow works
- [ ] Dashboard displays live data
- [ ] Charts update in real-time
- [ ] Responsive on mobile/tablet/desktop

### Integration Verification

- [ ] Frontend can authenticate with backend
- [ ] API calls succeed and return expected data
- [ ] SSE/polling keeps data fresh
- [ ] Error states display correctly
- [ ] Loading states work

### Documentation Verification

- [ ] Premium layer guide is complete
- [ ] Frontend README explains setup
- [ ] API endpoints are documented
- [ ] Troubleshooting section covers common issues

---

## Next Steps (Post-Implementation)

After this plan is complete, consider:

1. **Add more dashboard widgets** - Top nodes by traffic, alert history
2. **Implement wizard flows** - Guided node onboarding, user creation
3. **Add bulk operation UI** - Track progress, view results
4. **Build template management UI** - CRUD for templates/presets
5. **Add E2E tests** - Playwright tests for critical flows
6. **Performance optimization** - Bundle splitting, code splitting
7. **Mobile app** - React Native app using same API

---

## Summary

This plan implements a luxury dashboard layer on top of antimage:

**Backend additions (Go):**
- 4 new packages: `dashboard`, `templates`, `bulk`, wizard helpers
- 13 new HTTP endpoints across 3 categories
- 4 new database tables
- 2 background workers (sweeper + bulk processor)

**Frontend rebuild (Next.js):**
- Complete Next.js 15 app with React 19
- Dashboard-first layout with real-time widgets
- Type-safe API client
- Modern component library (shadcn/ui)

**Zero breaking changes** - all existing functionality remains intact.

**Timeline estimate:** 6-9 weeks for full implementation with polish and testing.

