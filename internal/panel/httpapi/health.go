package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// HealthResponse is the health check response.
type HealthResponse struct {
	Status    string            `json:"status"`
	Checks    map[string]string `json:"checks,omitempty"`
	Timestamp int64             `json:"timestamp"`
}

// handleHealth returns liveness status.
// GET /health
func (d Deps) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{
		Status:    "ok",
		Timestamp: time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleReady returns readiness status with component checks.
// GET /ready
func (d Deps) handleReady(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]string)
	allReady := true

	// Check database connectivity
	ctx := r.Context()
	err := d.Store.Read().PingContext(ctx)
	if err != nil {
		checks["database"] = "error: " + err.Error()
		allReady = false
	} else {
		// Verify database is writable
		var result int
		err = d.Store.Read().QueryRowContext(ctx, "SELECT 1").Scan(&result)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				checks["database"] = "ok"
			} else {
				checks["database"] = "error: " + err.Error()
				allReady = false
			}
		} else {
			checks["database"] = "ok"
		}
	}

	// Check hub (control plane)
	if d.Hub == nil {
		checks["hub"] = "error: not initialized"
		allReady = false
	} else {
		checks["hub"] = "ok"
	}

	resp := HealthResponse{
		Checks:    checks,
		Timestamp: time.Now().Unix(),
	}

	if allReady {
		resp.Status = "ready"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
	} else {
		resp.Status = "not_ready"
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	_ = json.NewEncoder(w).Encode(resp)
}
