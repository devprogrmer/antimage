package deployment

import (
	"context"
	"fmt"
	"net"

	"github.com/amyrm/antimage/internal/panel/rbac"
	"github.com/amyrm/antimage/internal/panel/store"
)

type Conflict struct {
	Type        string  `json:"type"`
	Description string  `json:"description"`
	NodeIDs     []int64 `json:"node_ids,omitempty"`
}

type Warning struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type ValidationResult struct {
	Valid     bool       `json:"valid"`
	Conflicts []Conflict `json:"conflicts"`
	Warnings  []Warning  `json:"warnings"`
}

type Validator struct {
	store *store.Store
}

func NewValidator(st *store.Store) *Validator {
	return &Validator{store: st}
}

func (v *Validator) ValidateRevision(ctx context.Context, nodeID int64, revisionNum int64) (*ValidationResult, error) {
	result := &ValidationResult{
		Valid:     true,
		Conflicts: []Conflict{},
		Warnings:  []Warning{},
	}

	var docSHA256 string
	err := v.store.Read().QueryRowContext(ctx,
		`SELECT doc_sha256 FROM node_revisions WHERE node_id = ? AND revision = ?`,
		nodeID, revisionNum).Scan(&docSHA256)
	if err != nil {
		return nil, fmt.Errorf("get revision: %w", err)
	}

	// Use super admin scope for internal validation
	scope := rbac.Scope{AdminID: 0, IsSuper: true}
	nodes, err := v.store.ListNodes(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	var targetNode *store.NodeRow
	for i := range nodes {
		if nodes[i].ID == nodeID {
			targetNode = &nodes[i]
			break
		}
	}

	if targetNode == nil {
		result.Valid = false
		result.Conflicts = append(result.Conflicts, Conflict{
			Type:        "node_not_found",
			Description: fmt.Sprintf("node %d not found", nodeID),
			NodeIDs:     []int64{nodeID},
		})
		return result, nil
	}

	return result, nil
}

func (v *Validator) ValidateDesiredState(ctx context.Context, desiredState map[string]interface{}) (*ValidationResult, error) {
	result := &ValidationResult{
		Valid:     true,
		Conflicts: []Conflict{},
		Warnings:  []Warning{},
	}

	// Use super admin scope for internal validation - needs to see all nodes
	scope := rbac.Scope{AdminID: 0, IsSuper: true}
	nodes, err := v.store.ListNodes(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	nodeMap := make(map[int64]*store.NodeRow)
	for i := range nodes {
		nodeMap[nodes[i].ID] = &nodes[i]
	}

	if err := v.validateNodeReferences(desiredState, nodeMap, result); err != nil {
		return nil, err
	}

	if err := v.validatePortConflicts(desiredState, result); err != nil {
		return nil, err
	}

	if err := v.validateProtocolConfigs(desiredState, result); err != nil {
		return nil, err
	}

	return result, nil
}

func (v *Validator) validateNodeReferences(desiredState map[string]interface{}, nodeMap map[int64]*store.NodeRow, result *ValidationResult) error {
	nodeConfigs, ok := desiredState["nodes"].(map[string]interface{})
	if !ok {
		return nil
	}

	for nodeIDStr := range nodeConfigs {
		var nodeID int64
		if _, err := fmt.Sscanf(nodeIDStr, "%d", &nodeID); err != nil {
			result.Valid = false
			result.Conflicts = append(result.Conflicts, Conflict{
				Type:        "invalid_node_id",
				Description: fmt.Sprintf("node ID '%s' is not a valid integer", nodeIDStr),
			})
			continue
		}

		if _, exists := nodeMap[nodeID]; !exists {
			result.Valid = false
			result.Conflicts = append(result.Conflicts, Conflict{
				Type:        "unknown_node",
				Description: fmt.Sprintf("node %d does not exist", nodeID),
				NodeIDs:     []int64{nodeID},
			})
		}
	}

	return nil
}

func (v *Validator) validatePortConflicts(desiredState map[string]interface{}, result *ValidationResult) error {
	nodeConfigs, ok := desiredState["nodes"].(map[string]interface{})
	if !ok {
		return nil
	}

	portUsage := make(map[string][]int64)

	for nodeIDStr, configIface := range nodeConfigs {
		var nodeID int64
		if _, err := fmt.Sscanf(nodeIDStr, "%d", &nodeID); err != nil {
			continue // Skip invalid node IDs
		}

		config, ok := configIface.(map[string]interface{})
		if !ok {
			continue
		}

		services, ok := config["services"].([]interface{})
		if !ok {
			continue
		}

		for _, serviceIface := range services {
			service, ok := serviceIface.(map[string]interface{})
			if !ok {
				continue
			}

			port, ok := service["port"].(float64)
			if !ok {
				continue
			}

			portKey := fmt.Sprintf("%d:%d", nodeID, int(port))
			portUsage[portKey] = append(portUsage[portKey], nodeID)
		}
	}

	for portKey, nodeIDs := range portUsage {
		if len(nodeIDs) > 1 {
			result.Valid = false
			result.Conflicts = append(result.Conflicts, Conflict{
				Type:        "port_conflict",
				Description: fmt.Sprintf("port %s is used by multiple services", portKey),
				NodeIDs:     nodeIDs,
			})
		}
	}

	return nil
}

func (v *Validator) validateProtocolConfigs(desiredState map[string]interface{}, result *ValidationResult) error {
	nodeConfigs, ok := desiredState["nodes"].(map[string]interface{})
	if !ok {
		return nil
	}

	for nodeIDStr, configIface := range nodeConfigs {
		var nodeID int64
		if _, err := fmt.Sscanf(nodeIDStr, "%d", &nodeID); err != nil {
			continue // Skip invalid node IDs
		}

		config, ok := configIface.(map[string]interface{})
		if !ok {
			continue
		}

		services, ok := config["services"].([]interface{})
		if !ok {
			continue
		}

		for i, serviceIface := range services {
			service, ok := serviceIface.(map[string]interface{})
			if !ok {
				continue
			}

			protocol, ok := service["protocol"].(string)
			if !ok {
				result.Valid = false
				result.Conflicts = append(result.Conflicts, Conflict{
					Type:        "missing_protocol",
					Description: fmt.Sprintf("node %d service %d missing protocol field", nodeID, i),
					NodeIDs:     []int64{nodeID},
				})
				continue
			}

			switch protocol {
			case "vless", "vmess", "trojan", "shadowsocks", "hysteria2":
			default:
				result.Warnings = append(result.Warnings, Warning{
					Type:        "unknown_protocol",
					Description: fmt.Sprintf("node %d service %d uses unknown protocol '%s'", nodeID, i, protocol),
				})
			}

			if portFloat, ok := service["port"].(float64); ok {
				port := int(portFloat)
				if port < 1 || port > 65535 {
					result.Valid = false
					result.Conflicts = append(result.Conflicts, Conflict{
						Type:        "invalid_port",
						Description: fmt.Sprintf("node %d service %d has invalid port %d (must be 1-65535)", nodeID, i, port),
						NodeIDs:     []int64{nodeID},
					})
				}
			}

			if listenIP, ok := service["listen"].(string); ok {
				if net.ParseIP(listenIP) == nil && listenIP != "0.0.0.0" && listenIP != "::" {
					result.Valid = false
					result.Conflicts = append(result.Conflicts, Conflict{
						Type:        "invalid_listen_ip",
						Description: fmt.Sprintf("node %d service %d has invalid listen IP '%s'", nodeID, i, listenIP),
						NodeIDs:     []int64{nodeID},
					})
				}
			}
		}
	}

	return nil
}

func (v *Validator) CheckNodeHealth(ctx context.Context, nodeIDs []int64) (map[int64]string, error) {
	healthStatus := make(map[int64]string)

	// Use super admin scope for internal health checks
	scope := rbac.Scope{AdminID: 0, IsSuper: true}
	nodes, err := v.store.ListNodes(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	nodeMap := make(map[int64]*store.NodeRow)
	for i := range nodes {
		nodeMap[nodes[i].ID] = &nodes[i]
	}

	for _, nodeID := range nodeIDs {
		node, exists := nodeMap[nodeID]
		if !exists {
			healthStatus[nodeID] = "unknown"
			continue
		}

		// A node is healthy if it's online
		switch node.Status {
		case "online":
			healthStatus[nodeID] = "healthy"
		case "offline", "disabled":
			healthStatus[nodeID] = "unhealthy"
		default:
			// pending, enrolling, degraded, integrity
			healthStatus[nodeID] = "degraded"
		}
	}

	return healthStatus, nil
}
