# Phase 3: Advanced User Management Implementation

**Objective:** Complete feature parity with competitors + advanced enterprise features

---

## User Management Enhancement Plan

### 1. Advanced Search & Filtering (4 hours)

**Backend API Extensions:**
- GET /api/v2/subjects with enhanced query params:
  - `search` - name/note/email search
  - `status` - active/disabled/frozen/expired
  - `expires_before` - filter by expiration date
  - `expires_after` - filter by expiration date
  - `traffic_min` - minimum traffic used
  - `traffic_max` - maximum traffic used
  - `quota_status` - under_limit/near_limit/over_limit
  - `online` - filter by currently connected
  - `sort` - name/created/expires/traffic
  - `order` - asc/desc

**Frontend Component:**
- SearchBar with debounced input
- FilterDropdowns for status, quota, online
- DateRangePicker for expiration
- TrafficRangeSlider for usage filtering
- SortControls for ordering
- ClearFilters button

### 2. Bulk Operations (3 hours)

**Backend Endpoints:**
- POST /api/v1/subjects/bulk/enable - Enable multiple subjects
- POST /api/v1/subjects/bulk/disable - Disable multiple subjects
- POST /api/v1/subjects/bulk/extend - Extend expiry for multiple
- POST /api/v1/subjects/bulk/reset-traffic - Reset traffic counters
- POST /api/v1/subjects/bulk/set-quota - Update quota for multiple

**Frontend Component:**
- Checkbox selection in subject table
- SelectAll/DeselectAll controls
- BulkActionMenu dropdown
- ConfirmationDialog for destructive actions
- ProgressBar for bulk operations
- ResultsSummary (succeeded/failed)

### 3. User Activity History (5 hours)

**Database Schema:**
```sql
CREATE TABLE subject_activity (
    id INTEGER PRIMARY KEY,
    subject_id INTEGER NOT NULL,
    event_type TEXT NOT NULL, -- login/logout/traffic/quota_exceeded
    timestamp INTEGER NOT NULL,
    details TEXT, -- JSON with event-specific data
    ip_address TEXT,
    device_id TEXT,
    node_id INTEGER,
    FOREIGN KEY (subject_id) REFERENCES subjects(id)
);

CREATE INDEX idx_subject_activity_subject ON subject_activity(subject_id, timestamp DESC);
CREATE INDEX idx_subject_activity_timestamp ON subject_activity(timestamp);
```

**Backend API:**
- GET /api/v1/subjects/{id}/activity - Activity timeline
- GET /api/v1/subjects/{id}/connections - Connection history
- GET /api/v1/subjects/{id}/devices - Device history

**Frontend Component:**
- ActivityTimeline with infinite scroll
- ConnectionTable with duration/traffic
- DeviceList with last seen/revoke button
- DateRangeFilter for activity

### 4. Enhanced Subscription System (3 hours)

**Backend Improvements:**
- Add subscription analytics tracking
- Add subscription revocation (regenerate token)
- Add traffic/expiry display in subscription response
- Add custom subscription URLs per user

**Frontend Component:**
- SubscriptionCard with QR code
- CopyButton for subscription URL
- RegenerateButton with confirmation
- SubscriptionAnalytics (views, last accessed)
- ExportButton (download config file)

---

## Implementation Order

### Week 1: Core Management Features

**Day 1-2: Advanced Search & Filtering**
1. Extend subjects_search.go with all query parameters
2. Add database indexes for performance
3. Create FilterBar.tsx component
4. Create SearchInput.tsx with debouncing
5. Add DateRangePicker and TrafficSlider components
6. Test with 10K+ subjects

**Day 3-4: Bulk Operations Backend**
1. Implement handleBulkEnable
2. Implement handleBulkDisable (already exists)
3. Implement handleBulkExtend with date calculation
4. Implement handleBulkResetTraffic with audit
5. Implement handleBulkSetQuota with validation
6. Add comprehensive tests

**Day 5: Bulk Operations Frontend**
1. Add checkbox column to subject table
2. Create BulkActionBar component
3. Create ConfirmationDialog component
4. Add progress tracking
5. Display results with success/failure counts

### Week 2: Activity & Subscription

**Day 1-2: User Activity System**
1. Create subject_activity table migration
2. Implement activity logging in enforcement engine
3. Create activity aggregation queries
4. Build ActivityTimeline component
5. Build ConnectionHistory component
6. Build DeviceHistory component

**Day 3: Enhanced Subscriptions**
1. Add subscription analytics table
2. Track subscription access in middleware
3. Implement subscription regeneration
4. Build SubscriptionCard UI
5. Add QR code download button

**Day 4-5: Integration & Polish**
1. Integrate all components into SubjectDetail
2. Add loading states everywhere
3. Add error boundaries
4. Performance optimization
5. E2E testing

---

## Database Migrations

### Migration: Add activity tracking
```sql
-- 001_add_subject_activity.sql
CREATE TABLE IF NOT EXISTS subject_activity (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subject_id INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    details TEXT,
    ip_address TEXT,
    device_id TEXT,
    node_id INTEGER,
    FOREIGN KEY (subject_id) REFERENCES subjects(id) ON DELETE CASCADE
);

CREATE INDEX idx_subject_activity_subject ON subject_activity(subject_id, timestamp DESC);
CREATE INDEX idx_subject_activity_timestamp ON subject_activity(timestamp DESC);
CREATE INDEX idx_subject_activity_type ON subject_activity(event_type, timestamp DESC);
```

### Migration: Add subscription analytics
```sql
-- 002_add_subscription_analytics.sql
CREATE TABLE IF NOT EXISTS subscription_analytics (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subject_id INTEGER NOT NULL,
    accessed_at INTEGER NOT NULL,
    user_agent TEXT,
    ip_address TEXT,
    format TEXT, -- v2ray/clash/singbox
    FOREIGN KEY (subject_id) REFERENCES subjects(id) ON DELETE CASCADE
);

CREATE INDEX idx_subscription_analytics_subject ON subscription_analytics(subject_id, accessed_at DESC);
```

---

## API Specification

### Advanced Search
```
GET /api/v2/subjects?search=john&status=active&expires_before=2026-12-31&traffic_min=1000000000&sort=traffic&order=desc&page=1&page_size=50

Response:
{
  "subjects": [...],
  "total": 150,
  "page": 1,
  "page_size": 50,
  "filters_applied": {
    "search": "john",
    "status": "active",
    "expires_before": "2026-12-31",
    "traffic_min": 1000000000
  }
}
```

### Bulk Operations
```
POST /api/v1/subjects/bulk/extend
{
  "subject_ids": [1, 2, 3],
  "days": 30
}

Response:
{
  "extended": 2,
  "failed": 1,
  "errors": ["subject 2: already expired"]
}
```

### Activity History
```
GET /api/v1/subjects/123/activity?from=2026-08-01&to=2026-08-31&limit=100

Response:
{
  "activities": [
    {
      "id": 1,
      "event_type": "login",
      "timestamp": 1692800000,
      "ip_address": "1.2.3.4",
      "device_id": "abc123",
      "node_id": 1,
      "details": {"protocol": "vless", "port": 443}
    }
  ],
  "total": 250,
  "has_more": true
}
```

---

## Testing Strategy

### Unit Tests
- Search query builder
- Filter logic
- Bulk operation transactions
- Activity logging
- Subscription regeneration

### Integration Tests
- Search with 10K subjects (< 100ms)
- Bulk operations with 1000 subjects (< 5s)
- Activity pagination
- Subscription analytics accuracy

### E2E Tests
- User searches and finds subjects
- User bulk-extends 100 subjects
- Activity timeline loads correctly
- Subscription QR code downloads

---

## Performance Targets

- Search: < 100ms (10K subjects)
- Filter: < 50ms per filter
- Bulk operations: < 5s (1000 subjects)
- Activity load: < 200ms (100 events)
- Pagination: < 30ms per page

---

## Success Criteria

✅ All searches complete in < 100ms  
✅ Bulk operations handle 1000 subjects  
✅ Activity history shows all events  
✅ Subscriptions track analytics  
✅ Frontend builds with no errors  
✅ All tests pass  
✅ No N+1 queries  
✅ Proper error handling everywhere  

---

**Starting implementation now...**
