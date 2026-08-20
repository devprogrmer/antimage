package observability

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/amyrm/antimage/internal/panel/nodes"
	"github.com/amyrm/antimage/internal/panel/store"
)

// Alert thresholds (from approved SP7 spec)
const (
	CertWarningThreshold  = 30 * 24 * time.Hour // 30 days
	CertCriticalThreshold = 7 * 24 * time.Hour  // 7 days
	QuotaWarningPercent   = 0.80                // 80%
	QuotaCriticalPercent  = 0.95                // 95%
)

// Sweeper periodically checks for certificate expiry and quota threshold conditions.
type Sweeper struct {
	store *store.Store
}

// NewSweeper creates a new background sweeper instance.
func NewSweeper(s *store.Store) *Sweeper {
	return &Sweeper{store: s}
}

// Run starts the background sweeper loop. Runs every 5 minutes until context is cancelled.
func (sw *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run immediately on start
	sw.sweep(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sw.sweep(ctx)
		}
	}
}

func (sw *Sweeper) sweep(ctx context.Context) {
	// Wrap in recover to prevent panic from crashing panel
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[observability] sweeper panic: %v", r)
		}
	}()

	now := time.Now().UTC()

	if err := sw.checkCertificates(ctx, now); err != nil {
		log.Printf("[observability] cert check failed: %v", err)
	}

	if err := sw.checkQuotas(ctx, now); err != nil {
		log.Printf("[observability] quota check failed: %v", err)
	}
}

func (sw *Sweeper) checkCertificates(ctx context.Context, now time.Time) error {
	// Query all nodes with certificates (enrolled_at IS NOT NULL means they have a cert)
	rows, err := sw.store.Read().QueryContext(ctx, `
		SELECT id, name, enrolled_at
		FROM nodes
		WHERE enrolled_at IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("query nodes with certificates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var nodeID int64
		var nodeName string
		var enrolledAtUnix int64

		if err := rows.Scan(&nodeID, &nodeName, &enrolledAtUnix); err != nil {
			log.Printf("[observability] scan node: %v", err)
			continue
		}

		enrolledAt := time.Unix(enrolledAtUnix, 0).UTC()
		// Certificate expires 1 year after enrollment (NodeCertLifetime from nodes/ca.go)
		certNotAfter := enrolledAt.Add(nodes.NodeCertLifetime)
		timeUntilExpiry := certNotAfter.Sub(now)

		// Check warning threshold (30 days)
		if timeUntilExpiry <= CertWarningThreshold {
			severity := SeverityWarning
			threshold := "30 days"

			// Check if it's actually critical (7 days)
			if timeUntilExpiry <= CertCriticalThreshold {
				severity = SeverityCritical
				threshold = "7 days"
			}

			daysRemaining := int(timeUntilExpiry.Hours() / 24)
			if daysRemaining < 0 {
				daysRemaining = 0
			}

			alert := Alert{
				AlertType:      AlertTypeCertExpiry,
				Severity:       severity,
				TargetType:     TargetNode,
				TargetID:       nodeID,
				DedupKey:       fmt.Sprintf("cert_expiry:node:%d:%s", nodeID, severity),
				ThresholdValue: threshold,
				CurrentValue:   fmt.Sprintf("%d days", daysRemaining),
				Metadata: map[string]interface{}{
					"node_name":       nodeName,
					"cert_not_after":  certNotAfter.Format(time.RFC3339),
					"days_remaining":  daysRemaining,
					"cert_fingerprint": "", // Could be populated from nodes.cert_fingerprint if needed
				},
			}

			if _, _, err := CreateOrUpdateAlert(ctx, sw.store, alert, now); err != nil {
				log.Printf("[observability] create/update cert alert for node %d: %v", nodeID, err)
			}
		} else {
			// Certificate is valid for more than 30 days, resolve any existing alerts
			for _, severity := range []Severity{SeverityWarning, SeverityCritical} {
				dedupKey := fmt.Sprintf("cert_expiry:node:%d:%s", nodeID, severity)
				if err := ResolveAlert(ctx, sw.store, dedupKey, now); err != nil {
					log.Printf("[observability] resolve cert alert for node %d: %v", nodeID, err)
				}
			}
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate nodes: %w", err)
	}

	return nil
}

func (sw *Sweeper) checkQuotas(ctx context.Context, now time.Time) error {
	// Query all subjects with quotas that are not frozen
	// (frozen_at IS NULL means quota enforcement is active)
	rows, err := sw.store.Read().QueryContext(ctx, `
		SELECT id, name, quota_bytes, quota_used_bytes, quota_reset_at
		FROM subjects
		WHERE quota_bytes IS NOT NULL AND frozen_at IS NULL`)
	if err != nil {
		return fmt.Errorf("query subjects with quotas: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var subjectID int64
		var subjectName string
		var quotaBytes, quotaUsedBytes int64
		var quotaResetAtUnix sql.NullInt64

		if err := rows.Scan(&subjectID, &subjectName, &quotaBytes, &quotaUsedBytes, &quotaResetAtUnix); err != nil {
			log.Printf("[observability] scan subject: %v", err)
			continue
		}

		if quotaBytes == 0 {
			continue // Avoid division by zero
		}

		usagePercent := float64(quotaUsedBytes) / float64(quotaBytes)

		// Check critical threshold (95%)
		if usagePercent >= QuotaCriticalPercent {
			alert := Alert{
				AlertType:      AlertTypeQuotaWarning,
				Severity:       SeverityCritical,
				TargetType:     TargetSubject,
				TargetID:       subjectID,
				DedupKey:       fmt.Sprintf("quota:subject:%d:critical", subjectID),
				ThresholdValue: "95%",
				CurrentValue:   fmt.Sprintf("%.1f%%", usagePercent*100),
				Metadata: map[string]interface{}{
					"subject_name":     subjectName,
					"quota_bytes":      quotaBytes,
					"quota_used_bytes": quotaUsedBytes,
					"percent_used":     usagePercent * 100,
				},
			}

			if quotaResetAtUnix.Valid {
				resetAt := time.Unix(quotaResetAtUnix.Int64, 0).UTC()
				alert.Metadata["quota_reset_at"] = resetAt.Format(time.RFC3339)
			}

			if _, _, err := CreateOrUpdateAlert(ctx, sw.store, alert, now); err != nil {
				log.Printf("[observability] create/update critical quota alert for subject %d: %v", subjectID, err)
			}

		} else if usagePercent >= QuotaWarningPercent {
			// Warning threshold (80%) - only if not critical
			alert := Alert{
				AlertType:      AlertTypeQuotaWarning,
				Severity:       SeverityWarning,
				TargetType:     TargetSubject,
				TargetID:       subjectID,
				DedupKey:       fmt.Sprintf("quota:subject:%d:warning", subjectID),
				ThresholdValue: "80%",
				CurrentValue:   fmt.Sprintf("%.1f%%", usagePercent*100),
				Metadata: map[string]interface{}{
					"subject_name":     subjectName,
					"quota_bytes":      quotaBytes,
					"quota_used_bytes": quotaUsedBytes,
					"percent_used":     usagePercent * 100,
				},
			}

			if quotaResetAtUnix.Valid {
				resetAt := time.Unix(quotaResetAtUnix.Int64, 0).UTC()
				alert.Metadata["quota_reset_at"] = resetAt.Format(time.RFC3339)
			}

			if _, _, err := CreateOrUpdateAlert(ctx, sw.store, alert, now); err != nil {
				log.Printf("[observability] create/update warning quota alert for subject %d: %v", subjectID, err)
			}

			// Resolve any critical alert if usage dropped below 95%
			criticalDedupKey := fmt.Sprintf("quota:subject:%d:critical", subjectID)
			if err := ResolveAlert(ctx, sw.store, criticalDedupKey, now); err != nil {
				log.Printf("[observability] resolve critical quota alert for subject %d: %v", subjectID, err)
			}

		} else {
			// Usage below 80%, resolve any existing alerts
			for _, severity := range []Severity{SeverityWarning, SeverityCritical} {
				dedupKey := fmt.Sprintf("quota:subject:%d:%s", subjectID, severity)
				if err := ResolveAlert(ctx, sw.store, dedupKey, now); err != nil {
					log.Printf("[observability] resolve quota alert for subject %d: %v", subjectID, err)
				}
			}
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate subjects: %w", err)
	}

	return nil
}
