package subscriptions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// SingBoxRenderer renders sing-box format JSON subscriptions.
type SingBoxRenderer struct{}

// Render returns sing-box JSON config and content-type.
func (r *SingBoxRenderer) Render(ctx context.Context, servers []Server) ([]byte, string, error) {
	if len(servers) == 0 {
		return nil, "", fmt.Errorf("no servers to render")
	}

	var outbounds []map[string]interface{}
	var outboundTags []string

	// Add each server as an outbound
	for _, srv := range servers {
		outbound, err := r.renderServer(srv)
		// A protocol this format cannot express is SKIPPED, not fatal. The
		// loop used to abort the whole document, so a user holding one VLESS
		// inbound and one WireGuard inbound received an empty subscription --
		// and nothing anywhere said why. The per-inbound view in the panel is
		// where the omission is explained.
		if errors.Is(err, ErrNotRepresentable) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("render server %s: %w", srv.NodeName, err)
		}
		outbounds = append(outbounds, outbound)
		// The selector's tag list is built from what was ACTUALLY rendered,
		// not from the servers we were handed. Iterating the input meant a
		// skipped protocol still appeared as a tag, so sing-box was told to
		// select an outbound that is not in the document.
		outboundTags = append(outboundTags, srv.NodeName)
	}

	// Every server was unrepresentable in this format. Returning an error
	// beats indexing outboundTags[0] and panicking, and beats a document whose
	// selector has no options.
	if len(outboundTags) == 0 {
		return nil, "", fmt.Errorf(
			"none of this subject's inbounds can be expressed in a sing-box configuration")
	}

	selector := map[string]interface{}{
		"type":      "selector",
		"tag":       "proxy",
		"outbounds": outboundTags,
		"default":   outboundTags[0],
	}

	outbounds = append([]map[string]interface{}{selector}, outbounds...)

	// Add direct and block outbounds
	outbounds = append(outbounds,
		map[string]interface{}{"type": "direct", "tag": "direct"},
		map[string]interface{}{"type": "block", "tag": "block"},
	)

	config := map[string]interface{}{
		"outbounds": outbounds,
		"route": map[string]interface{}{
			"rules": []map[string]interface{}{
				{
					"geoip":    []string{"private"},
					"outbound": "direct",
				},
				{
					"geoip":    []string{"cn"},
					"outbound": "direct",
				},
				{
					"geosite":  []string{"cn"},
					"outbound": "direct",
				},
			},
			"final": "proxy",
		},
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
	case "hysteria2":
		return singboxHysteria2(srv)
	case "shadowsocks":
		return singboxShadowsocks(srv)
	default:
		return nil, ErrNotRepresentable
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

	// TLS
	if srv.TLS {
		tls := map[string]interface{}{
			"enabled": true,
		}
		if srv.SNI != "" {
			tls["server_name"] = srv.SNI
		}
		if len(srv.ALPN) > 0 {
			tls["alpn"] = srv.ALPN
		}
		outbound["tls"] = tls
	}

	// Transport
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

	// TLS
	if srv.TLS {
		tls := map[string]interface{}{
			"enabled": true,
		}
		if srv.SNI != "" {
			tls["server_name"] = srv.SNI
		}
		if len(srv.ALPN) > 0 {
			tls["alpn"] = srv.ALPN
		}
		outbound["tls"] = tls
	}

	// Transport
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

	// TLS (Trojan requires TLS)
	if srv.TLS {
		tls := map[string]interface{}{
			"enabled": true,
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
