package subscriptions

import (
	"context"
	"encoding/json"
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

	for _, srv := range servers {
		outbound, err := r.renderServer(srv)
		if err != nil {
			return nil, "", fmt.Errorf("render server %s: %w", srv.Label(), err)
		}
		outbounds = append(outbounds, outbound)
	}

	var outboundTags []string
	for _, srv := range servers {
		outboundTags = append(outboundTags, srv.Label())
	}

	selector := map[string]interface{}{
		"type":      "selector",
		"tag":       "proxy",
		"outbounds": outboundTags,
		"default":   outboundTags[0],
	}

	outbounds = append([]map[string]interface{}{selector}, outbounds...)

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

func (r *SingBoxRenderer) renderVLESS(srv Server) map[string]interface{} {
	outbound := map[string]interface{}{
		"type":        "vless",
		"tag":         srv.Label(),
		"server":      srv.NodeAddress,
		"server_port": srv.Port,
		"uuid":        srv.UUID,
	}

	network := srv.Network
	if network == "" {
		network = "tcp"
	}
	outbound["network"] = network
	if srv.Flow != "" {
		outbound["flow"] = srv.Flow
	}

	applySingBoxTLS(outbound, srv)
	applySingBoxTransport(outbound, srv, network)
	return outbound
}

func (r *SingBoxRenderer) renderVMess(srv Server) map[string]interface{} {
	outbound := map[string]interface{}{
		"type":        "vmess",
		"tag":         srv.Label(),
		"server":      srv.NodeAddress,
		"server_port": srv.Port,
		"uuid":        srv.UUID,
		"alter_id":    0,
		"security":    "auto",
	}

	network := srv.Network
	if network == "" {
		network = "tcp"
	}
	outbound["network"] = network

	applySingBoxTLS(outbound, srv)
	applySingBoxTransport(outbound, srv, network)
	return outbound
}

func (r *SingBoxRenderer) renderTrojan(srv Server) map[string]interface{} {
	outbound := map[string]interface{}{
		"type":        "trojan",
		"tag":         srv.Label(),
		"server":      srv.NodeAddress,
		"server_port": srv.Port,
		"password":    srv.Password,
	}

	applySingBoxTLS(outbound, srv)
	return outbound
}

func applySingBoxTransport(outbound map[string]interface{}, srv Server, network string) {
	switch network {
	case "ws":
		transport := map[string]interface{}{"type": "ws"}
		if srv.Path != "" {
			transport["path"] = srv.Path
		}
		if srv.Host != "" {
			transport["headers"] = map[string]interface{}{"Host": srv.Host}
		}
		outbound["transport"] = transport
	case "grpc":
		transport := map[string]interface{}{"type": "grpc"}
		if srv.Path != "" {
			transport["service_name"] = srv.Path
		}
		outbound["transport"] = transport
	}
}

func applySingBoxTLS(outbound map[string]interface{}, srv Server) {
	sec := srv.security()
	if sec != "tls" && sec != "reality" {
		return
	}
	tls := map[string]interface{}{"enabled": true}
	if srv.SNI != "" {
		tls["server_name"] = srv.SNI
	}
	if len(srv.ALPN) > 0 {
		tls["alpn"] = srv.ALPN
	}
	if srv.AllowInsecure {
		tls["insecure"] = true
	}
	if sec == "reality" {
		reality := map[string]interface{}{"enabled": true}
		if srv.PublicKey != "" {
			reality["public_key"] = srv.PublicKey
		}
		if srv.ShortID != "" {
			reality["short_id"] = srv.ShortID
		}
		tls["reality"] = reality
		fp := srv.Fingerprint
		if fp == "" {
			fp = "chrome"
		}
		tls["utls"] = map[string]interface{}{"enabled": true, "fingerprint": fp}
	} else if srv.Fingerprint != "" {
		tls["utls"] = map[string]interface{}{"enabled": true, "fingerprint": srv.Fingerprint}
	}
	outbound["tls"] = tls
}
