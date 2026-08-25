package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/amyrm/antimage/internal/panel/auth"
	"github.com/amyrm/antimage/internal/panel/control"
	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
	"github.com/amyrm/antimage/internal/shared/secrets"
)

type Deps struct {
	Store    *store.Store
	Sessions *auth.Sessions
	Limiter  *auth.Limiter
	Hub      *control.Hub
	CA       *nodes.CA
	// Box decrypts per-admin TOTP secrets. Nil means the master key is not
	// loaded: handleLogin then DENIES any admin who has TOTP enrolled rather
	// than admitting them on a password alone. Task 27's main.go populates it.
	Box *secrets.Box
	// DownloadDir holds published agent binaries served at /download/{name}.
	// Empty means nothing is published and every download 404s with a message
	// telling the operator where to put them.
	DownloadDir string
	// SSEInterval is how often GET /api/v1/events pushes a node-status
	// snapshot. Zero means defaultSSEInterval, which is what production
	// runs; it is a field only so a test can drive the loop faster than a
	// real client would rather than pin a sleep to the production value.
	SSEInterval time.Duration
	Now         func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now()
}

// loadActor resolves permissions and scope allow-lists once per request.
func (d Deps) loadActor(ctx context.Context, adminID int64) (*rbac.Actor, error) {
	var (
		roleName string
		rawPerms string
	)
	err := d.Store.Read().QueryRowContext(ctx,
		`SELECT r.name, r.permissions
		   FROM admins a JOIN roles r ON r.id = a.role_id
		  WHERE a.id = ? AND a.status = 'active'`, adminID).Scan(&roleName, &rawPerms)
	if err != nil {
		return nil, fmt.Errorf("load admin %d: %w", adminID, err)
	}

	var perms []rbac.Permission
	if err := json.Unmarshal([]byte(rawPerms), &perms); err != nil {
		return nil, fmt.Errorf("decode permissions: %w", err)
	}

	actor := &rbac.Actor{
		AdminID:    adminID,
		RoleName:   roleName,
		IsSuper:    roleName == "super_admin",
		Perms:      make(map[rbac.Permission]struct{}, len(perms)),
		NodeIDs:    map[int64]struct{}{},
		ServiceIDs: map[int64]struct{}{},
	}
	for _, p := range perms {
		actor.Perms[p] = struct{}{}
	}

	rows, err := d.Store.Read().QueryContext(ctx,
		`SELECT scope_type, scope_id FROM admin_scopes WHERE admin_id = ?`, adminID)
	if err != nil {
		return nil, fmt.Errorf("load scopes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var id int64
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, fmt.Errorf("scan scope: %w", err)
		}
		switch kind {
		case "node":
			actor.NodeIDs[id] = struct{}{}
		case "service":
			actor.ServiceIDs[id] = struct{}{}
		}
	}
	return actor, rows.Err()
}

// NewRouter builds the panel handler.
//
// Middleware order is deliberate: the request ID is minted first so both the
// panic log and every error envelope can be correlated to it, recovery sits
// immediately inside it so a panic anywhere below still produces a correlated
// 500, and the origin check runs before any handler work.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware, recoverMiddleware, originMiddleware)

	// Rate limiter: 1000 requests per minute per admin
	limiter := newRateLimiter(1000, time.Minute)

	r.Route("/api/v1", func(api chi.Router) {
		api.Post("/auth/login", d.handleLogin)

		// Unauthenticated on purpose, alongside GET /install.sh: a node being
		// bootstrapped has no session, and the CA fingerprint is a public
		// value — it is the thing the node pins the panel against.
		api.Get("/ca-fingerprint", d.handleCAFingerprint)

		// SP4: Subscription endpoint - public, token-authenticated.
		// The token IS the authentication, no session required.
		api.Get("/subscribe/{token}", d.handleSubscribe)
		api.Get("/subscribe/{token}/qr", d.handleSubscriptionQR)

		api.Group(func(private chi.Router) {
			private.Use(d.authMiddleware, d.readOnlyMiddleware, d.rateLimitMiddleware(limiter))

			private.Post("/auth/logout", d.handleLogout)
			private.Get("/auth/me", d.handleMe)

			// Each of these acts on the caller's own account only — no admin
			// id in the path, so there is no other account to authorize
			// against and no rbac.Check to make.
			private.Post("/auth/totp/enrol", d.handleTOTPEnrol)
			private.Post("/auth/totp/confirm", d.handleTOTPConfirm)
			private.Post("/auth/totp/disable", d.handleTOTPDisable)

			// Telegram linking is self-service for the same reason: the admin
			// id comes from the session, never from the request, so there is
			// no other account to authorize against. Accepting an admin_id
			// here would let any authenticated caller bind THEIR chat account
			// to somebody else's panel user.
			private.Get("/me/telegram", d.handleGetMyTelegram)
			private.Post("/me/telegram/link", d.handleCreateTelegramLinkCode)
			private.Delete("/me/telegram", d.handleDeleteMyTelegram)

			// Tenancy. The engine behind these has existed since the reseller
			// engine landed; it simply had no routes, so none of it was
			// reachable. credit:grant is separate from reseller:write on every
			// one of them -- minting credit is the only operation that creates
			// value from nothing.
			private.Get("/me/reseller", d.handleGetMyReseller)
			// Self-service, resolved from the session. No reseller id in the
			// path, so a tenant cannot ask for another tenant's history.
			private.Get("/me/reseller/ledger", d.handleGetMyLedger)
			private.Get("/resellers", d.handleListResellers)
			private.Post("/resellers", d.handleCreateReseller)
			private.Get("/resellers/{resellerID}", d.handleGetReseller)
			private.Put("/resellers/{resellerID}", d.handleUpdateReseller)
			private.Get("/resellers/{resellerID}/balance", d.handleGetResellerBalance)
			private.Get("/resellers/{resellerID}/ledger", d.handleListResellerLedger)
			private.Delete("/resellers/{resellerID}", d.handleDeleteReseller)
			private.Post("/resellers/{resellerID}/credit", d.handleGrantCredit)
			// Provisioning: the operation the engine exists for. Until now it
			// was reachable only through CSV import.
			private.Post("/resellers/{resellerID}/subjects", d.handleProvisionSubject)

			private.Get("/nodes", d.handleListNodes)
			private.Post("/nodes", d.handleCreateNode)
			private.Get("/nodes/{nodeID}", d.handleGetNode)
			private.Delete("/nodes/{nodeID}", d.handleDeleteNode)
			private.Post("/nodes/{nodeID}/enroll-token", d.handleIssueEnrollToken)
			private.Post("/nodes/{nodeID}/bootstrap-ssh", d.handleSSHBootstrap)
			private.Get("/nodes/{nodeID}/revisions", d.handleListRevisions)
			private.Get("/nodes/{nodeID}/apply-runs", d.handleListApplyRuns)
			private.Get("/nodes/{nodeID}/adapters", d.handleListAdapters) // SP5: adapter registry
			private.Get("/nodes/{nodeID}/metrics", d.handleNodeMetrics)   // SP5: connection metrics
			private.Get("/nodes/{nodeID}/history", d.handleNodeHistory)   // SP7: metrics history
			private.Get("/nodes/{nodeID}/health/latest", d.handleGetNodeHealthLatest)
			private.Get("/nodes/{nodeID}/health/history", d.handleGetNodeHealthHistory)
			private.Get("/nodes/{nodeID}/reconciliation", d.handleGetNodeReconciliation)
			private.Get("/nodes/{nodeID}/capabilities", d.handleGetNodeCapabilities)
			// What this node can actually serve, and what each protocol needs.
			// Built from the node's own Hello rather than the panel's compiled-in
			// adapter list, so an editor offers only what the node can execute.
			private.Get("/nodes/{nodeID}/service-schemas", d.handleListServiceSchemas)

			// Node actions (M6)
			private.Post("/nodes/{nodeID}/restart", d.handleRestartNode)
			private.Post("/nodes/{nodeID}/sync", d.handleSyncNode)
			private.Post("/nodes/{nodeID}/maintenance", d.handleSetNodeMaintenance)
			private.Post("/nodes/{nodeID}/enable", d.handleEnableNode)
			private.Post("/nodes/{nodeID}/disable", d.handleDisableNode)

			// Bulk node operations (M7)
			private.Post("/nodes/bulk/action", d.handleBulkNodeAction)

			// Services could be created, updated and deleted but never read, so
			// nothing could show what a node is already serving.
			private.Get("/nodes/{nodeID}/services", d.handleListServices)
			private.Post("/nodes/{nodeID}/services", d.handleCreateService)

			// Egress. Node-scoped, because an outbound is a path off one host
			// and a routing rule selects between the outbounds that host has.
			// Every mutation runs through CommitNodeChange, so configuring
			// egress bumps the node's revision like any other desired-state
			// change rather than leaving the panel holding a policy the node
			// was never told about.
			private.Get("/nodes/{nodeID}/egress/capabilities", d.handleGetEgressCapabilities)
			private.Get("/nodes/{nodeID}/outbounds", d.handleListOutbounds)
			private.Post("/nodes/{nodeID}/outbounds", d.handleCreateOutbound)
			private.Put("/nodes/{nodeID}/outbounds/{outboundID}", d.handleUpdateOutbound)
			private.Delete("/nodes/{nodeID}/outbounds/{outboundID}", d.handleDeleteOutbound)

			private.Get("/nodes/{nodeID}/routing", d.handleListRoutingRules)
			private.Post("/nodes/{nodeID}/routing", d.handleCreateRoutingRule)
			private.Put("/nodes/{nodeID}/routing/{ruleID}", d.handleUpdateRoutingRule)
			private.Delete("/nodes/{nodeID}/routing/{ruleID}", d.handleDeleteRoutingRule)
			// Where unmatched traffic goes. PUT rather than POST: there is
			// exactly one per node and setting it twice is the same as setting
			// it once.
			private.Put("/nodes/{nodeID}/routing/default", d.handleSetDefaultOutbound)
			private.Put("/services/{serviceID}", d.handleUpdateService)
			private.Delete("/services/{serviceID}", d.handleDeleteService)

			// SP7: Observability API
			private.Get("/alerts", d.handleListAlerts)
			private.Get("/fleet/summary", d.handleFleetSummary)

			// Premium: Dashboard endpoints
			private.Get("/dashboard/overview", d.handleDashboardOverview)
			private.Get("/dashboard/stream", d.handleDashboardStream)
			private.Get("/dashboard/traffic-chart", d.handleDashboardTrafficChart)
			private.Get("/dashboard/top-users", d.handleDashboardTopUsers)

			// Service template routes
			private.Get("/templates/services", d.handleListServiceTemplates)
			private.Post("/templates/services", d.handleCreateServiceTemplate)
			private.Get("/templates/services/{id}", d.handleGetServiceTemplate)
			private.Put("/templates/services/{id}", d.handleUpdateServiceTemplate)
			private.Delete("/templates/services/{id}", d.handleDeleteServiceTemplate)

			// User preset routes
			private.Get("/presets/users", d.handleListUserPresets)
			private.Post("/presets/users", d.handleCreateUserPreset)
			private.Get("/presets/users/{id}", d.handleGetUserPreset)
			private.Put("/presets/users/{id}", d.handleUpdateUserPreset)
			private.Delete("/presets/users/{id}", d.handleDeleteUserPreset)

			// Subjects are the people a node serves. Credentials are never
			// returned by list or get; revealing one needs its own permission
			// and is audited by kind, never by value.
			private.Get("/subjects", d.handleListSubjects)
			private.Post("/subjects", d.handleCreateSubject)
			private.Get("/subjects/{subjectID}", d.handleGetSubject)
			private.Put("/subjects/{subjectID}", d.handleUpdateSubject)
			private.Delete("/subjects/{subjectID}", d.handleDeleteSubject)
			private.Get("/subjects/{subjectID}/credentials/{kind}", d.handleRevealCredential)
			private.Post("/subjects/{subjectID}/credentials/{kind}/rotate", d.handleRotateCredential)
			private.Get("/subjects/export", d.handleExportSubjects)
			private.Post("/subjects/import", d.handleImportSubjects)
			private.Post("/subjects/bulk/delete", d.handleBulkDeleteSubjects)
			private.Post("/subjects/bulk/enable", d.handleBulkEnableSubjects)
			private.Post("/subjects/bulk/extend", d.handleBulkExtendSubjects)
			private.Post("/subjects/bulk/reset-traffic", d.handleBulkResetTraffic)
			private.Post("/subjects/bulk/set-quota", d.handleBulkSetQuota)

			// Subject lifecycle operations
			private.Post("/subjects/{subjectID}/freeze", d.handleFreezeSubject)
			private.Post("/subjects/{subjectID}/unfreeze", d.handleUnfreezeSubject)
			private.Post("/subjects/{subjectID}/disable", d.handleDisableSubject)
			private.Post("/subjects/{subjectID}/enable", d.handleEnableSubject)

			// Device management and enforcement endpoints
			private.Get("/subjects/{id}/devices", d.handleListDevices)
			private.Get("/subjects/{id}/connections", d.handleListActiveConnections)
			private.Get("/subjects/{id}/enforcement", d.handleGetEnforcementStatus)
			private.Post("/devices/{id}/revoke", d.handleRevokeDevice)

			private.Get("/audit", d.handleListAudit)
			private.Get("/sessions", d.handleListSessions)
			private.Delete("/sessions/{sessionID}", d.handleRevokeSession)

			// Deployment orchestration endpoints
			private.Post("/deployments/validate", d.handleDeploymentValidate)
			private.Post("/deployments/preview", d.handleDeploymentPreview)
			private.Post("/deployments", d.handleDeploymentCreate)
			private.Get("/deployments", d.handleDeploymentList)
			private.Get("/deployments/{id}", d.handleDeploymentGet)
			private.Post("/deployments/{id}/rollback", d.handleDeploymentRollback)

			private.Get("/events", d.handleEvents)
		})
	})

	// API v2 carries one endpoint so far: the paginated, filterable subject
	// list. It was written as v2 throughout -- handleListSubjectsV2,
	// SubjectListV2Response, and a doc comment naming /api/v2/subjects -- but
	// registered inside the v1 group, so it was actually served from
	// /api/v1/v2/subjects. Nothing called it, which is why the path survived.
	//
	// Same middleware as v1's private group. A newer API version is not a less
	// protected one, and the handler's own permission and scope checks assume
	// an authenticated actor is already in the context.
	r.Route("/api/v2", func(api chi.Router) {
		api.Group(func(private chi.Router) {
			private.Use(d.authMiddleware, d.readOnlyMiddleware, d.rateLimitMiddleware(limiter))
			private.Get("/subjects", d.handleListSubjectsV2)
		})
	})

	// Registered before the UI catch-all so the SPA handler cannot shadow it.
	r.Get("/install.sh", d.handleInstallScript)

	// Unauthenticated for the same reason as /install.sh: a node running the
	// bootstrap one-liner has no session, and the agent binary is not secret.
	r.Get("/download/{name}", d.handleDownload)
	r.Head("/download/{name}", d.handleDownload)

	// SP5: Prometheus metrics endpoint (no auth, standard for /metrics)
	r.Handle("/metrics", promhttp.Handler())

	// Health check endpoints (no auth, for orchestration)
	r.Get("/health", d.handleHealth)
	r.Get("/ready", d.handleReady)

	r.Handle("/*", d.uiHandler())
	return r
}

// snapshotOpts turns the configured secret box into BuildDesiredSnapshot
// options, so every commit that rebuilds a desired document can unseal the
// credentials of subjects on that node.
//
// Without this, the first subject created on a node makes every subsequent
// commit for it fail: the document builder refuses to omit subjects it cannot
// unseal, which is correct, but it must be given the means to unseal them.
func (d Deps) snapshotOpts() []nodes.SnapshotOption {
	if d.Box == nil {
		return nil
	}
	return []nodes.SnapshotOption{nodes.WithUnsealer(d.Box)}
}
