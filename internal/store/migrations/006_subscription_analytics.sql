-- Add subscription analytics for tracking access
CREATE TABLE IF NOT EXISTS subscription_analytics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subject_id INTEGER NOT NULL,
    accessed_at INTEGER NOT NULL,
    user_agent TEXT,
    ip_address TEXT,
    format TEXT, -- v2ray/clash/singbox/raw
    FOREIGN KEY (subject_id) REFERENCES subjects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_subscription_analytics_subject ON subscription_analytics(subject_id, accessed_at DESC);
CREATE INDEX IF NOT EXISTS idx_subscription_analytics_timestamp ON subscription_analytics(accessed_at DESC);
