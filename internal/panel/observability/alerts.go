// Package observability provides alert management and metrics rollup functionality for SP7.
package observability

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

// AlertType identifies the kind of alert condition.
type AlertType string

const (
	AlertTypeCertExpiry    AlertType = "cert_expiry"
	AlertTypeQuotaWarning  AlertType = "quota_warning"
	AlertTypeQuotaExceeded AlertType = "quota_exceeded"
)

// Severity indicates alert urgency.
type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// AlertState tracks alert lifecycle.
type AlertState string

const (
	StateActive   AlertState = "active"
	StateResolved AlertState = "resolved"
)

// TargetType identifies what the alert applies to.
type TargetType string

const (
	TargetNode    TargetType = "node"
	TargetSubject TargetType = "subject"
)

// Alert represents a persistent alert condition with full lifecycle tracking.
type Alert struct {
	ID             int64
	AlertType      AlertType
	Severity       Severity
	TargetType     TargetType
	TargetID       int64
	State          AlertState
	DedupKey       string
	FirstSeenAt    time.Time
	LastSeenAt     time.Time
	ResolvedAt     *time.Time
	ThresholdValue string
	CurrentValue   string
	Metadata       map[string]interface{}
}

// createOrUpdateAlertTx is the internal transaction-based implementation.
func createOrUpdateAlertTx(ctx context.Context, tx *sql.Tx, a Alert, now time.Time) (int64, bool, error) {
	// Check if active alert with this dedup_key already exists
	var existingID sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM alerts
		WHERE dedup_key = ? AND state = 'active'`,
		a.DedupKey).Scan(&existingID)

	if errors.Is(err, sql.ErrNoRows) {
		// No active alert exists, create new one
		metadataJSON, err := json.Marshal(a.Metadata)
		if err != nil {
			return 0, false, fmt.Errorf("marshal metadata: %w", err)
		}

		res, err := tx.ExecContext(ctx, `
			INSERT INTO alerts (
				alert_type, severity, target_type, target_id,
				state, dedup_key, first_seen_at, last_seen_at,
				threshold_value, current_value, metadata
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(a.AlertType), string(a.Severity), string(a.TargetType), a.TargetID,
			string(StateActive), a.DedupKey, now.Unix(), now.Unix(),
			a.ThresholdValue, a.CurrentValue, string(metadataJSON))
		if err != nil {
			return 0, false, fmt.Errorf("insert alert: %w", err)
		}

		alertID, err := res.LastInsertId()
		if err != nil {
			return 0, false, fmt.Errorf("get last insert id: %w", err)
		}
		return alertID, true, nil
	}

	if err != nil {
		return 0, false, fmt.Errorf("check existing alert: %w", err)
	}

	// Active alert exists, update last_seen_at and current values
	alertID := existingID.Int64

	metadataJSON, err := json.Marshal(a.Metadata)
	if err != nil {
		return 0, false, fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE alerts
		SET last_seen_at = ?,
		    current_value = ?,
		    metadata = ?
		WHERE id = ?`,
		now.Unix(), a.CurrentValue, string(metadataJSON), alertID)
	if err != nil {
		return 0, false, fmt.Errorf("update alert: %w", err)
	}

	return alertID, false, nil
}

// CreateOrUpdateAlert creates a new alert or updates last_seen_at if already active.
// Returns (alert_id, created=true/false, error).
//
// Behavior:
// - If no active alert with this dedup_key exists: INSERT new alert, return (id, true, nil)
// - If active alert exists: UPDATE last_seen_at, current_value, metadata, return (existing_id, false, nil)
// - Resolved alerts with same dedup_key do not prevent creation (re-alert supported)
func CreateOrUpdateAlert(ctx context.Context, s *store.Store, a Alert, now time.Time) (int64, bool, error) {
	var alertID int64
	var created bool

	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		alertID, created, err = createOrUpdateAlertTx(ctx, tx, a, now)
		return err
	})

	if err != nil {
		return 0, false, err
	}

	return alertID, created, nil
}

// CreateOrUpdateAlertTx is a transaction-based version that can be called within an existing transaction.
func CreateOrUpdateAlertTx(ctx context.Context, tx *sql.Tx, a Alert, now time.Time) (int64, bool, error) {
	return createOrUpdateAlertTx(ctx, tx, a, now)
}

// ResolveAlert marks an active alert as resolved. Idempotent: if the alert is already
// resolved or doesn't exist, no error is returned.
func ResolveAlert(ctx context.Context, s *store.Store, dedupKey string, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE alerts
			SET state = 'resolved', resolved_at = ?
			WHERE dedup_key = ? AND state = 'active'`,
			now.Unix(), dedupKey)
		if err != nil {
			return fmt.Errorf("resolve alert: %w", err)
		}
		// No error if no rows updated (idempotent)
		return nil
	})
}

// AlertFilters specifies query criteria for ListAlerts.
type AlertFilters struct {
	Scope      rbac.Scope // Required: enforces admin scope filtering
	State      AlertState // Filter by state (required: 'active', 'resolved', or empty for all)
	AlertType  AlertType  // Filter by alert type (optional)
	Severity   Severity   // Filter by severity (optional)
	TargetType TargetType // Filter by target type (optional)
	TargetID   *int64     // Filter by specific target ID (optional)
	Limit      int        // Max results (default 50, max 200)
	Offset     int        // Pagination offset
}

// ListAlerts queries alerts with filtering, pagination, and scope enforcement.
// Returns (alerts, total_count, error).
// Enforces admin_scopes: non-super admins only see alerts for accessible nodes/subjects.
func ListAlerts(ctx context.Context, s *store.Store, filters AlertFilters) ([]Alert, int, error) {
	// Apply defaults and limits
	if filters.Limit == 0 {
		filters.Limit = 50
	}
	if filters.Limit > 200 {
		filters.Limit = 200
	}

	var alerts []Alert
	var totalCount int

	err := s.Read().QueryRowContext(ctx, buildCountQuery(filters), buildQueryArgs(filters, false)...).Scan(&totalCount)
	if err != nil {
		return nil, 0, fmt.Errorf("count alerts: %w", err)
	}

	rows, err := s.Read().QueryContext(ctx, buildSelectQuery(filters), buildQueryArgs(filters, true)...)
	if err != nil {
		return nil, 0, fmt.Errorf("query alerts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var a Alert
		var alertType, severity, targetType, state, metadataJSON string
		var firstSeenUnix, lastSeenUnix int64
		var resolvedAtUnix sql.NullInt64

		err := rows.Scan(
			&a.ID, &alertType, &severity, &targetType, &a.TargetID,
			&state, &a.DedupKey, &firstSeenUnix, &lastSeenUnix, &resolvedAtUnix,
			&a.ThresholdValue, &a.CurrentValue, &metadataJSON)
		if err != nil {
			return nil, 0, fmt.Errorf("scan alert: %w", err)
		}

		a.AlertType = AlertType(alertType)
		a.Severity = Severity(severity)
		a.TargetType = TargetType(targetType)
		a.State = AlertState(state)
		a.FirstSeenAt = time.Unix(firstSeenUnix, 0).UTC()
		a.LastSeenAt = time.Unix(lastSeenUnix, 0).UTC()
		if resolvedAtUnix.Valid {
			resolvedAt := time.Unix(resolvedAtUnix.Int64, 0).UTC()
			a.ResolvedAt = &resolvedAt
		}

		if err := json.Unmarshal([]byte(metadataJSON), &a.Metadata); err != nil {
			return nil, 0, fmt.Errorf("unmarshal metadata for alert %d: %w", a.ID, err)
		}

		alerts = append(alerts, a)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate alerts: %w", err)
	}

	return alerts, totalCount, nil
}

func buildSelectQuery(filters AlertFilters) string {
	query := `
		SELECT id, alert_type, severity, target_type, target_id, state,
		       dedup_key, first_seen_at, last_seen_at, resolved_at,
		       threshold_value, current_value, metadata
		FROM alerts
		WHERE 1=1`

	query += buildWhereClause(filters)
	query += ` ORDER BY first_seen_at DESC LIMIT ? OFFSET ?`
	return query
}

func buildCountQuery(filters AlertFilters) string {
	query := `SELECT COUNT(*) FROM alerts WHERE 1=1`
	query += buildWhereClause(filters)
	return query
}

func buildWhereClause(filters AlertFilters) string {
	var where string

	// Scope filtering: non-super admins only see alerts for their accessible targets
	if !filters.Scope.IsSuper {
		// For node targets: filter by admin_scopes.node_id
		// For subject targets: filter by subjects owned by admin (via admin_scopes on nodes serving those subjects)
		where += ` AND (
			(alerts.target_type = 'node' AND EXISTS (
				SELECT 1 FROM admin_scopes
				WHERE admin_scopes.admin_id = ? AND admin_scopes.node_id = alerts.target_id
			))
			OR
			(alerts.target_type = 'subject' AND EXISTS (
				SELECT 1 FROM subjects
				JOIN services ON services.subject_id = subjects.id
				JOIN nodes ON services.node_id = nodes.id
				JOIN admin_scopes ON admin_scopes.node_id = nodes.id
				WHERE subjects.id = alerts.target_id AND admin_scopes.admin_id = ?
			))
		)`
	}

	if filters.State != "" {
		where += ` AND state = ?`
	}
	if filters.AlertType != "" {
		where += ` AND alert_type = ?`
	}
	if filters.Severity != "" {
		where += ` AND severity = ?`
	}
	if filters.TargetType != "" {
		where += ` AND target_type = ?`
	}
	if filters.TargetID != nil {
		where += ` AND target_id = ?`
	}

	return where
}

func buildQueryArgs(filters AlertFilters, includePagination bool) []interface{} {
	var args []interface{}

	// Add scope args if not super admin
	if !filters.Scope.IsSuper {
		args = append(args, filters.Scope.AdminID, filters.Scope.AdminID)
	}

	if filters.State != "" {
		args = append(args, string(filters.State))
	}
	if filters.AlertType != "" {
		args = append(args, string(filters.AlertType))
	}
	if filters.Severity != "" {
		args = append(args, string(filters.Severity))
	}
	if filters.TargetType != "" {
		args = append(args, string(filters.TargetType))
	}
	if filters.TargetID != nil {
		args = append(args, *filters.TargetID)
	}

	if includePagination {
		args = append(args, filters.Limit, filters.Offset)
	}

	return args
}
