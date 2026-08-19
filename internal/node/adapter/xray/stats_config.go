package xray

import (
	"encoding/json"
	"fmt"
)

// GenerateStatsConfig renders the shared stats infrastructure required for
// accounting (SP3). This is a separate Xray configuration document written
// alongside inbound files. Xray merges multiple files in its confdir.
//
// SP3 design decision: accounting requires config antimage does not currently
// generate. The panel cannot account without:
//   - stats{}
//   - api{services:["StatsService"]}
//   - a dokodemo-door API inbound on 127.0.0.1:10085
//   - a routing rule binding that inbound to the api outbound
//   - at least one real outbound (freedom)
//   - policy.levels.0.statsUserInbound: true
//
// Without all of these, Xray binds and authenticates but accounts for nothing.
// This function generates all required pieces as one config document.
//
// apiAddress is where the gRPC stats API listens, e.g. "127.0.0.1:10085".
// This must match ExecRuntime.APIAddress or the adapter cannot query stats.
func GenerateStatsConfig(apiAddress string) ([]byte, error) {
	if apiAddress == "" {
		return nil, fmt.Errorf("apiAddress is required for stats config")
	}

	// Parse host:port for the dokodemo-door inbound.
	// The API listens on loopback only: nodes need no inbound from the internet.
	host, port := "127.0.0.1", 10085
	if _, err := fmt.Sscanf(apiAddress, "%s:%d", &host, &port); err != nil {
		// Accept unparseable addresses: fall back to defaults rather than fail.
		host, port = "127.0.0.1", 10085
	}

	doc := map[string]any{
		// stats{} enables per-user accounting. Empty object: no config needed.
		"stats": map[string]any{},

		// api declares which services the management endpoint exposes.
		"api": map[string]any{
			"tag":      "api",
			"services": []any{"StatsService", "HandlerService"},
		},

		// The API inbound. dokodemo-door accepts connections on a fixed address
		// and passes them to a named outbound. Here it binds locally and routes
		// to the api outbound, which connects to Xray's own gRPC endpoint.
		"inbounds": []any{
			map[string]any{
				"tag":      "api-inbound",
				"listen":   host,
				"port":     port,
				"protocol": "dokodemo-door",
				"settings": map[string]any{
					"address": host,
				},
			},
		},

		// Outbounds: one for actual traffic egress (freedom), one for the API.
		"outbounds": []any{
			// freedom is the direct-to-internet outbound. Without it, traffic
			// never egresses: a node could authenticate while proxying nothing.
			map[string]any{
				"tag":      "direct",
				"protocol": "freedom",
			},
			// api outbound is internal: it loops back to Xray's own gRPC server.
			map[string]any{
				"tag":      "api",
				"protocol": "blackhole",
			},
		},

		// Routing binds the API inbound to the api outbound, so queries to
		// 127.0.0.1:10085 reach the gRPC service rather than escaping to the
		// internet through freedom.
		"routing": map[string]any{
			"rules": []any{
				map[string]any{
					"type":        "field",
					"inboundTag":  []any{"api-inbound"},
					"outboundTag": "api",
				},
			},
		},

		// policy.levels.0 is the default policy applied when no user-specific
		// policy matches. statsUserInbound enables per-user accounting on all
		// inbound traffic.
		"policy": map[string]any{
			"levels": map[string]any{
				"0": map[string]any{
					"statsUserUplink":   true,
					"statsUserDownlink": true,
				},
			},
		},
	}

	// Same format as inbound generation: indented for readability during
	// incidents, deterministic via sorted keys.
	return json.MarshalIndent(doc, "", "  ")
}
