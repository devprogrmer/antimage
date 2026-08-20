package subscriptions

import (
	"context"
	"encoding/json"
	"fmt"
)

// SingBoxRenderer renders sing-box-format JSON subscriptions.
type SingBoxRenderer struct{}

// Render returns sing-box JSON config and content-type.
func (r *SingBoxRenderer) Render(ctx context.Context, servers []Server) ([]byte, string, error) {
	if len(servers) == 0 {
		return nil, "", fmt.Errorf("no servers to render")
	}

	var outbounds []map[string]interface{}
	for _, srv := range servers {
		outbound, err := r.renderServer(srv)
		if err != nil {
			return nil, "", fmt.Errorf("render server %s: %w", srv.NodeName, err)
		}
		outbounds = append(outbounds, outbound)
	}

	config := map[string]interface{}{
		"outbounds": outbounds,
	}

	jsonBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("marshal json: %w", err)
	}

	return jsonBytes, "application/json; charset=utf-8", nil
}

// renderServer converts a Server to a sing-box outbound.
func (r *SingBoxRenderer) renderServer(srv Server) (map[string]interface{}, error) {
	switch srv.Protocol {
	case "vless":
		return r.renderVLESS(srv), nil
	case "vmess":
		return r.renderVMess(srv), nil
	case "trojan":
		return r.renderTrojan(srv), nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", srv.Protocol)
	}
}

// renderVLESS generates a sing-box VLESS outbound.
func (r *SingBoxRenderer) renderVLESS(srv Server) map[string]interface{} {
	outbound := map[string]interface{}{
		"type":        "vless",
		"tag":         srv.NodeName,
		"server":      srv.NodeAddress,
		"server_port": srv.Port,
		"uuid":        srv.UUID,
	}

	// Network type
	network := srv.Network
	if network == "" {
		network = "tcp"
	}
	outbound["network"] = network

	// TLS configuration
	if srv.TLS {
		tls := map[string]interface{}{
			"enabled":  true,
			"insecure": false,
		}
		if srv.SNI != "" {
			tls["server_name"] = srv.SNI
		}
		if len(srv.ALPN) > 0 {
			tls["alpn"] = srv.ALPN
		}
		outbound["tls"] = tls
	}

	// Transport configuration
	switch network {
	case "ws":
		transport := map[string]interface{}{
			"type": "ws",
		}
		if srv.Path != "" {
			transport["path"] = srv.Path
		}
		outbound["transport"] = transport
	case "grpc":
		transport := map[string]interface{}{
			"type": "grpc",
		}
		if srv.Path != "" {
			transport["service_name"] = srv.Path
		}
		outbound["transport"] = transport
	}

	return outbound
}

// renderVMess generates a sing-box VMess outbound.
func (r *SingBoxRenderer) renderVMess(srv Server) map[string]interface{} {
	outbound := map[string]interface{}{
		"type":        "vmess",
		"tag":         srv.NodeName,
		"server":      srv.NodeAddress,
		"server_port": srv.Port,
		"uuid":        srv.UUID,
		"alter_id":    0,
		"security":    "auto",
	}

	// Network type
	network := srv.Network
	if network == "" {
		network = "tcp"
	}
	outbound["network"] = network

	// TLS configuration
	if srv.TLS {
		tls := map[string]interface{}{
			"enabled":  true,
			"insecure": false,
		}
		if srv.SNI != "" {
			tls["server_name"] = srv.SNI
		}
		if len(srv.ALPN) > 0 {
			tls["alpn"] = srv.ALPN
		}
		outbound["tls"] = tls
	}

	// Transport configuration
	switch network {
	case "ws":
		transport := map[string]interface{}{
			"type": "ws",
		}
		if srv.Path != "" {
			transport["path"] = srv.Path
		}
		outbound["transport"] = transport
	case "grpc":
		transport := map[string]interface{}{
			"type": "grpc",
		}
		if srv.Path != "" {
			transport["service_name"] = srv.Path
		}
		outbound["transport"] = transport
	}

	return outbound
}

// renderTrojan generates a sing-box Trojan outbound.
func (r *SingBoxRenderer) renderTrojan(srv Server) map[string]interface{} {
	outbound := map[string]interface{}{
		"type":        "trojan",
		"tag":         srv.NodeName,
		"server":      srv.NodeAddress,
		"server_port": srv.Port,
		"password":    srv.Password,
	}

	// Trojan typically requires TLS
	if srv.TLS {
		tls := map[string]interface{}{
			"enabled":  true,
			"insecure": false,
		}
		if srv.SNI != "" {
			tls["server_name"] = srv.SNI
		}
		if len(srv.ALPN) > 0 {
			tls["alpn"] = srv.ALPN
		}
		outbound["tls"] = tls
	}

	return outbound
}
