# Phase 2: Real-Time Dashboard Implementation

**Objective:** Add WebSocket/SSE for live metrics updates

## Current Status

Panel builds successfully with all P0 enterprise features:
- ✅ QR code generation
- ✅ Search/filter/pagination
- ✅ CSV import/export
- ✅ Bulk delete
- ✅ Health checks
- ✅ Docker deployment

## Real-Time Dashboard Requirements

### Backend Components

1. **WebSocket/SSE Handler**
   - Endpoint: GET /api/v1/dashboard/stream
   - Authentication: Session-based
   - Protocol: Server-Sent Events (simpler than WebSocket)
   - Update frequency: Every 5 seconds

2. **Metrics Aggregation**
   - Active users count (per node, total)
   - Traffic statistics (upload/download rates)
   - Node status (online/offline/degraded)
   - System metrics (CPU, RAM, disk from nodes)
   - Connection counts

3. **Data Sources**
   - Database queries for subject counts
   - Node metrics from gRPC
   - Hub state for live connections
   - Adapter stats from Xray/WireGuard

### Frontend Components

1. **Dashboard Page**
   - Grid layout with metric cards
   - Real-time updating charts
   - Status indicators for nodes
   - Alert notifications

2. **Chart Library**
   - Library: recharts (already used in ecosystem)
   - Chart types: Line (traffic), Bar (top users), Pie (protocol distribution)
   - Auto-scaling axes
   - Time-series data handling

3. **EventSource Integration**
   - Connect to SSE endpoint on mount
   - Parse JSON events
   - Update component state
   - Reconnect on disconnect

## Implementation Plan

### Step 1: Backend SSE Handler (1 hour)
- Create `dashboard_stream.go` with SSE handler
- Query active connections from database
- Aggregate traffic stats from nodes
- Format as JSON events
- Send every 5 seconds with heartbeat

### Step 2: Metrics Collection (2 hours)
- Add system metrics collection to node agent
- Extend gRPC protocol with metrics RPC
- Store metrics in database (time-series table)
- Create aggregation queries

### Step 3: Frontend Dashboard Component (3 hours)
- Create `Dashboard.tsx` React component
- Add recharts dependency
- Create metric cards for overview
- Implement EventSource connection
- Add loading and error states

### Step 4: Chart Components (2 hours)
- Traffic chart (line chart, last 24h)
- Top users chart (bar chart, top 10)
- Protocol distribution (pie chart)
- Node status grid

## API Design

### GET /api/v1/dashboard/stream

**Response:** text/event-stream

```
event: metrics
data: {"timestamp": 1692800000, "active_users": 42, "nodes_online": 3, "traffic_mbps": 125.5}

event: traffic
data: {"upload_mbps": 65.2, "download_mbps": 60.3, "total_gb": 1250}

event: nodes
data: [{"id": 1, "name": "us-east-1", "status": "online", "cpu": 45, "ram": 60, "users": 15}]

event: heartbeat
data: {"timestamp": 1692800005}
```

### GET /api/v1/dashboard/overview

**Response:** application/json

```json
{
  "active_users": 42,
  "total_subjects": 150,
  "nodes_online": 3,
  "nodes_total": 5,
  "traffic_today_gb": 25.5,
  "bandwidth_mbps": 125.5,
  "alerts_count": 2,
  "frozen_count": 5
}
```

## Database Schema Extension

```sql
CREATE TABLE IF NOT EXISTS node_metrics (
    node_id INTEGER NOT NULL,
    timestamp INTEGER NOT NULL,
    cpu_percent REAL,
    ram_percent REAL,
    disk_percent REAL,
    bandwidth_mbps REAL,
    connection_count INTEGER,
    PRIMARY KEY (node_id, timestamp)
);

CREATE INDEX idx_node_metrics_timestamp ON node_metrics(timestamp);
```

## Next Actions

1. Implement SSE handler with basic metrics
2. Test with curl/browser
3. Add frontend EventSource integration
4. Build chart components
5. Polish UI/UX

**Estimated completion: 8 hours**
