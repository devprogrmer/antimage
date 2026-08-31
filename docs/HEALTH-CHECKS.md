# Health Check Endpoints

Antimage provides health check endpoints for monitoring and orchestration.

## Endpoints

### Liveness Probe

**GET /health**

Returns 200 if the service is running.

```bash
curl http://localhost:8080/health
```

Response:
```json
{
  "status": "ok",
  "timestamp": 1692800000
}
```

### Readiness Probe

**GET /ready**

Returns 200 if the service is ready to accept requests (database connected, etc.).

```bash
curl http://localhost:8080/ready
```

Response when ready:
```json
{
  "status": "ready",
  "checks": {
    "database": "ok",
    "hub": "ok"
  },
  "timestamp": 1692800000
}
```

Response when not ready (503):
```json
{
  "status": "not_ready",
  "checks": {
    "database": "error: connection refused",
    "hub": "ok"
  },
  "timestamp": 1692800000
}
```

## Kubernetes Configuration

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: antimage-panel
spec:
  containers:
  - name: panel
    image: antimage-panel:latest
    ports:
    - containerPort: 8080
    livenessProbe:
      httpGet:
        path: /health
        port: 8080
      initialDelaySeconds: 30
      periodSeconds: 10
      timeoutSeconds: 5
      failureThreshold: 3
    readinessProbe:
      httpGet:
        path: /ready
        port: 8080
      initialDelaySeconds: 10
      periodSeconds: 5
      timeoutSeconds: 3
      failureThreshold: 2
```

## Docker Compose Configuration

```yaml
services:
  panel:
    image: antimage-panel:latest
    healthcheck:
      test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://localhost:8080/health"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 40s
```

## Monitoring Integration

### Prometheus

Health status is also exported as metrics:

```
# HELP antimage_health_status Health check status (1 = healthy, 0 = unhealthy)
# TYPE antimage_health_status gauge
antimage_health_status{check="database"} 1
antimage_health_status{check="hub"} 1
```

### Alertmanager

Alert on unhealthy status:

```yaml
groups:
  - name: antimage
    rules:
      - alert: AntimagePanelUnhealthy
        expr: antimage_health_status == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Antimage panel health check failing"
          description: "Health check {{ $labels.check }} has been failing for 2 minutes"
```
