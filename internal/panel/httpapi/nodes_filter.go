package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

// GET /api/v1/nodes?status=online&protocol=xray&online=true&search=prod
func (d Deps) handleListNodesFiltered(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireActor(w, r)
	if !ok {
		return
	}

	if !d.authorize(w, r, actor, rbac.PermNodeRead, rbac.Target{Kind: rbac.TargetNone}) {
		return
	}

	// Get all nodes in scope
	ctx := r.Context()
	scope := rbac.ScopeOf(actor)
	rows, err := d.Store.ListNodes(ctx, scope)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "internal", "could not list nodes")
		return
	}

	// Parse query parameters
	query := r.URL.Query()
	filters := NodeFilters{
		Status:    query.Get("status"),
		Protocol:  query.Get("protocol"),
		MinCPU:    parseFloatParam(query.Get("min_cpu")),
		MaxCPU:    parseFloatParam(query.Get("max_cpu")),
		MinMemory: parseIntParam(query.Get("min_memory")),
		MaxMemory: parseIntParam(query.Get("max_memory")),
		MinDisk:   parseIntParam(query.Get("min_disk")),
		MaxDisk:   parseIntParam(query.Get("max_disk")),
		Online:    parseBoolParam(query.Get("online")),
		Search:    query.Get("search"),
	}

	// Apply filters
	var filtered []nodeSummary
	for _, node := range rows {
		if d.matchesFilters(ctx, node, filters) {
			filtered = append(filtered, d.summarize(node))
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{"nodes": filtered, "count": len(filtered)})
}

type NodeFilters struct {
	Status    string
	Protocol  string
	MinCPU    *float64
	MaxCPU    *float64
	MinMemory *int64
	MaxMemory *int64
	MinDisk   *int64
	MaxDisk   *int64
	Online    *bool
	Search    string
}

func parseBoolParam(s string) *bool {
	if s == "" {
		return nil
	}
	b := s == "true" || s == "1"
	return &b
}

func parseFloatParam(s string) *float64 {
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

func parseIntParam(s string) *int64 {
	if s == "" {
		return nil
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &i
}

func (d Deps) matchesFilters(ctx context.Context, node store.NodeRow, filters NodeFilters) bool {
	// Status filter
	if filters.Status != "" && node.Status != filters.Status {
		return false
	}

	// Search filter (name or address)
	if filters.Search != "" {
		searchLower := strings.ToLower(filters.Search)
		if !strings.Contains(strings.ToLower(node.Name), searchLower) &&
			!strings.Contains(strings.ToLower(node.Address), searchLower) {
			return false
		}
	}

	// Online filter
	if filters.Online != nil {
		isOnline := d.Hub.Online(node.ID)
		if *filters.Online != isOnline {
			return false
		}
	}

	// Protocol filter
	if filters.Protocol != "" {
		if !d.nodeHasProtocol(ctx, node.ID, filters.Protocol) {
			return false
		}
	}

	// Metrics-based filters
	if filters.MinCPU != nil || filters.MaxCPU != nil ||
		filters.MinMemory != nil || filters.MaxMemory != nil ||
		filters.MinDisk != nil || filters.MaxDisk != nil {

		metrics := d.getLatestMetricsForFilter(ctx, node.ID)
		if metrics == nil {
			// No metrics available, exclude from results
			return false
		}

		// CPU filters
		if filters.MinCPU != nil && (metrics.CPUPercent == nil || *metrics.CPUPercent < *filters.MinCPU) {
			return false
		}
		if filters.MaxCPU != nil && (metrics.CPUPercent == nil || *metrics.CPUPercent > *filters.MaxCPU) {
			return false
		}

		// Memory filters
		if filters.MinMemory != nil && (metrics.MemoryUsedBytes == nil || *metrics.MemoryUsedBytes < *filters.MinMemory) {
			return false
		}
		if filters.MaxMemory != nil && (metrics.MemoryUsedBytes == nil || *metrics.MemoryUsedBytes > *filters.MaxMemory) {
			return false
		}

		// Disk filters
		if filters.MinDisk != nil && (metrics.DiskUsedBytes == nil || *metrics.DiskUsedBytes < *filters.MinDisk) {
			return false
		}
		if filters.MaxDisk != nil && (metrics.DiskUsedBytes == nil || *metrics.DiskUsedBytes > *filters.MaxDisk) {
			return false
		}
	}

	return true
}

func (d Deps) nodeHasProtocol(ctx context.Context, nodeID int64, protocol string) bool {
	var available int
	err := d.Store.Read().QueryRowContext(ctx, `
		SELECT available FROM node_capabilities
		WHERE node_id = ? AND protocol = ? AND available = 1
	`, nodeID, protocol).Scan(&available)
	return err == nil && available == 1
}

func (d Deps) getLatestMetricsForFilter(ctx context.Context, nodeID int64) *metricsSnapshot {
	var m metricsSnapshot
	err := d.Store.Read().QueryRowContext(ctx, `
		SELECT cpu_percent, memory_used_bytes, disk_used_bytes
		FROM node_metrics
		WHERE node_id = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`, nodeID).Scan(&m.CPUPercent, &m.MemoryUsedBytes, &m.DiskUsedBytes)

	if err != nil {
		return nil
	}
	return &m
}

type metricsSnapshot struct {
	CPUPercent      *float64
	MemoryUsedBytes *int64
	DiskUsedBytes   *int64
}
