package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ClashRenderer renders Clash-format YAML subscriptions.
type ClashRenderer struct{}

// Render returns Clash YAML config and content-type.
func (r *ClashRenderer) Render(ctx context.Context, servers []Server) ([]byte, string, error) {
	if len(servers) == 0 {
		return nil, "", fmt.Errorf("no servers to render")
	}

	var proxies []map[string]interface{}
	for _, srv := range servers {
		proxy, err := r.renderServer(srv)
		// A protocol this format cannot express is SKIPPED, not fatal. The
		// loop used to abort the whole document, so a user holding one VLESS
		// inbound and one WireGuard inbound received an empty subscription --
		// and nothing anywhere said why. The per-inbound view in the panel is
		// where the omission is explained.
		if errors.Is(err, ErrNotRepresentable) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("render server %s: %w", srv.Label(), err)
		}
		proxies = append(proxies, proxy)
	}

	// Nothing this format can express. Saying so beats a valid YAML document
	// with an empty proxy list, which gives the user nothing and explains
	// nothing.
	if len(proxies) == 0 {
		return nil, "", fmt.Errorf(
			"none of this subject's inbounds can be expressed in a Clash configuration")
	}

	config := map[string]interface{}{
		"proxies": proxies,
	}

	yamlBytes, err := yaml.Marshal(config)
	if err != nil {
		return nil, "", fmt.Errorf("marshal yaml: %w", err)
	}

	return yamlBytes, "application/x-yaml; charset=utf-8", nil
}

// renderServer converts a Server to a Clash proxy map.
func (r *ClashRenderer) renderServer(srv Server) (map[string]interface{}, error) {
	switch srv.Protocol {
	case "vless":
		return r.renderVLESS(srv), nil
	case "vmess":
		return r.renderVMess(srv), nil
	case "trojan":
		return r.renderTrojan(srv), nil
	case "hysteria2":
		return clashHysteria2(srv)
	case "shadowsocks":
		return clashShadowsocks(srv)
	default:
		return nil, ErrNotRepresentable
	}
}

// renderVLESS generates a Clash VLESS proxy.
func (r *ClashRenderer) renderVLESS(srv Server) map[string]interface{} {
	proxy := map[string]interface{}{
		"name":   srv.Label(),
		"type":   "vless",
		"server": srv.NodeAddress,
		"port":   srv.Port,
		"uuid":   srv.UUID,
		"udp":    true,
	}

	// Network type
	network := srv.Network
	if network == "" {
		network = "tcp"
	}
	proxy["network"] = network

	applyClashSecurity(proxy, srv)

	if len(srv.ALPN) > 0 {
		proxy["alpn"] = srv.ALPN
	}
	if srv.Flow != "" {
		proxy["flow"] = srv.Flow
	}

	if network == "ws" {
		wsOpts := make(map[string]interface{})
		if srv.Path != "" {
			wsOpts["path"] = srv.Path
		}
		if srv.Host != "" {
			wsOpts["headers"] = map[string]interface{}{"Host": srv.Host}
		}
		if len(wsOpts) > 0 {
			proxy["ws-opts"] = wsOpts
		}
	}

	if network == "grpc" && srv.Path != "" {
		proxy["grpc-opts"] = map[string]interface{}{
			"grpc-service-name": strings.TrimPrefix(srv.Path, "/"),
		}
	}

	return proxy
}

// renderVMess generates a Clash VMess proxy.
func (r *ClashRenderer) renderVMess(srv Server) map[string]interface{} {
	proxy := map[string]interface{}{
		"name":    srv.Label(),
		"type":    "vmess",
		"server":  srv.NodeAddress,
		"port":    srv.Port,
		"uuid":    srv.UUID,
		"alterId": 0,
		"cipher":  "auto",
		"udp":     true,
	}

	// Network type
	network := srv.Network
	if network == "" {
		network = "tcp"
	}
	proxy["network"] = network

	// TLS
	if srv.TLS {
		proxy["tls"] = true
		proxy["skip-cert-verify"] = false
		if srv.SNI != "" {
			proxy["servername"] = srv.SNI
		}
	}

	// ALPN
	if len(srv.ALPN) > 0 {
		proxy["alpn"] = srv.ALPN
	}

	// WebSocket options
	if network == "ws" {
		wsOpts := make(map[string]interface{})
		if srv.Path != "" {
			wsOpts["path"] = srv.Path
		}
		if len(wsOpts) > 0 {
			proxy["ws-opts"] = wsOpts
		}
	}

	// gRPC options
	if network == "grpc" && srv.Path != "" {
		grpcOpts := map[string]interface{}{
			"grpc-service-name": srv.Path,
		}
		proxy["grpc-opts"] = grpcOpts
	}

	return proxy
}

// renderTrojan generates a Clash Trojan proxy.
func (r *ClashRenderer) renderTrojan(srv Server) map[string]interface{} {
	proxy := map[string]interface{}{
		"name":     srv.Label(),
		"type":     "trojan",
		"server":   srv.NodeAddress,
		"port":     srv.Port,
		"password": srv.Password,
		"udp":      true,
	}

	// Trojan typically requires TLS
	if srv.TLS {
		proxy["skip-cert-verify"] = false
		if srv.SNI != "" {
			proxy["sni"] = srv.SNI
		}
	}

	// ALPN
	if len(srv.ALPN) > 0 {
		proxy["alpn"] = srv.ALPN
	}

	return proxy
}

func applyClashSecurity(proxy map[string]interface{}, srv Server) {
	sec := srv.security()
	switch sec {
	case "tls":
		proxy["tls"] = true
		proxy["skip-cert-verify"] = srv.AllowInsecure
		if srv.SNI != "" {
			proxy["servername"] = srv.SNI
		}
		if srv.Fingerprint != "" {
			proxy["client-fingerprint"] = srv.Fingerprint
		}
	case "reality":
		proxy["tls"] = true
		proxy["skip-cert-verify"] = true
		if srv.SNI != "" {
			proxy["servername"] = srv.SNI
		}
		fp := srv.Fingerprint
		if fp == "" {
			fp = "chrome"
		}
		proxy["client-fingerprint"] = fp
		reality := map[string]interface{}{}
		if srv.PublicKey != "" {
			reality["public-key"] = srv.PublicKey
		}
		if srv.ShortID != "" {
			reality["short-id"] = srv.ShortID
		}
		if len(reality) > 0 {
			proxy["reality-opts"] = reality
		}
	}
}
