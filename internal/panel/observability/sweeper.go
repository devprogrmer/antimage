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

	// Freezing for quota deliberately does NOT happen here. It belongs to
	// nodes.QuotaEnforcementSweeper, which is the only sweeper that completes
	// the job: it sets enabled = 0 alongside frozen_at and commits a node
	// change so the agent actually stops serving the subject.
	//
	// This package used to run its own enforceQuotaFreeze, and it was worse
	// than redundant. It stamped frozen_at while leaving enabled = 1, and
	// findSubjectsOverQuota selects `AND s.frozen_at IS NULL` -- so a subject
	// it touched was excluded from the real enforcer permanently. Since Run()
	// sweeps immediately on start and the quota enforcer waits a full five
	// minutes for its first tick, every subject already over quota at panel
	// start lost that race. It also measured raw bytes, contradicting the C4
	// decision that quota enforces on billable.
	//
	// Alerting is still this package's job -- alertQuotaExceeded below raises
	// the quota_exceeded alert the removed function used to raise, without
	// touching the subject. Acting on it is the control plane's.

	if err := sw.alertQuotaExceeded(ctx, now); err != nil {
		log.Printf("[observability] quota exceeded alerting failed: %v", err)
	}

	if err := sw.enforceQuotaWarnings(ctx); err != nil {
		log.Printf("[observability] quota warnings failed: %v", err)
	}

	if err := sw.checkNodeHealth(ctx, now); err != nil {
		log.Printf("[observability] node health check failed: %v", err)
	}

	if err := sw.checkEnforcementFailures(ctx, now); err != nil {
		log.Printf("[observability] enforcement failure check failed: %v", err)
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
					"node_name":        nodeName,
					"cert_not_after":   certNotAfter.Format(time.RFC3339),
					"days_remaining":   daysRemaining,
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

// alertQuotaExceeded raises a critical alert for every subject at or past its
// quota. It reads; it never writes to subjects.
//
// This is the surviving half of the old enforceQuotaFreeze. That function
// raised this same alert and also froze the subject, and the freezing half was
// both incomplete and actively harmful (see sweep). The alert is what an
// operator needs and is kept; the mutation belongs to
// nodes.QuotaEnforcementSweeper alone.
//
// Unlike checkQuotas this does NOT skip frozen subjects. Once the enforcer has
// cut a subject off, "this subject is over quota" is still true, and an alert
// that disappeared at the moment it was acted on would leave the operator
// unable to see why service stopped.
func (sw *Sweeper) alertQuotaExceeded(ctx context.Context, now time.Time) error {
	rows, err := sw.store.Read().QueryContext(ctx, `
		SELECT id, name, quota_bytes, quota_used_bytes
		FROM subjects
		WHERE quota_bytes IS NOT NULL
		  AND quota_bytes > 0
		  AND quota_used_bytes >= quota_bytes`)
	if err != nil {
		return fmt.Errorf("query subjects over quota: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type over struct {
		id          int64
		name        string
		quota, used int64
	}
	var subjects []over
	for rows.Next() {
		var s over
		if err := rows.Scan(&s.id, &s.name, &s.quota, &s.used); err != nil {
			log.Printf("[observability] scan over-quota subject: %v", err)
			continue
		}
		subjects = append(subjects, s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate subjects: %w", err)
	}

	for _, s := range subjects {
		alert := Alert{
			AlertType:      AlertTypeQuotaExceeded,
			Severity:       SeverityCritical,
			TargetType:     TargetSubject,
			TargetID:       s.id,
			DedupKey:       fmt.Sprintf("quota_exceeded:subject:%d", s.id),
			ThresholdValue: fmt.Sprintf("%d bytes", s.quota),
			CurrentValue:   fmt.Sprintf("%d bytes", s.used),
			Metadata: map[string]interface{}{
				"subject_name":     s.name,
				"quota_bytes":      s.quota,
				"quota_used_bytes": s.used,
			},
		}
		if _, _, err := CreateOrUpdateAlert(ctx, sw.store, alert, now); err != nil {
			log.Printf("[observability] create quota exceeded alert for subject %d: %v", s.id, err)
		}
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
