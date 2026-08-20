package subscriptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Server represents one node+credential pair to render in subscription configs.
type Server struct {
	NodeID      int64
	NodeName    string
	NodeAddress string
	ServiceID   int64
	Protocol    string // "vless", "vmess", "trojan"
	Port        int
	UUID        string // For VLESS/VMess
	Password    string // For Trojan
	TLS         bool
	SNI         string
	ALPN        []string
	Network     string // "tcp", "ws", "grpc"
	Path        string // WebSocket/HTTP path
}

// V2RayRenderer renders v2ray-format subscriptions (base64-encoded URI lines).
type V2RayRenderer struct{}

// Render returns base64-encoded subscription content and content-type.
func (r *V2RayRenderer) Render(ctx context.Context, servers []Server) ([]byte, string, error) {
	if len(servers) == 0 {
		return nil, "", fmt.Errorf("no servers to render")
	}

	var lines []string
	for _, srv := range servers {
		uri, err := r.renderServer(srv)
		if err != nil {
			return nil, "", fmt.Errorf("render server %s: %w", srv.NodeName, err)
		}
		lines = append(lines, uri)
	}

	// Join lines and base64-encode the result.
	content := strings.Join(lines, "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	return []byte(encoded), "text/plain; charset=utf-8", nil
}

// renderServer generates a single v2ray URI (vless://, vmess://, or trojan://).
func (r *V2RayRenderer) renderServer(srv Server) (string, error) {
	switch srv.Protocol {
	case "vless":
		return r.renderVLESS(srv), nil
	case "vmess":
		return r.renderVMess(srv)
	case "trojan":
		return r.renderTrojan(srv), nil
	default:
		return "", fmt.Errorf("unsupported protocol: %s", srv.Protocol)
	}
}

// renderVLESS generates a vless:// URI.
// Format: vless://uuid@host:port?type=tcp&security=tls&sni=example.com#name
func (r *V2RayRenderer) renderVLESS(srv Server) string {
	var params []string

	// Network type
	network := srv.Network
	if network == "" {
		network = "tcp"
	}
	params = append(params, fmt.Sprintf("type=%s", network))

	// TLS
	if srv.TLS {
		params = append(params, "security=tls")
		if srv.SNI != "" {
			params = append(params, fmt.Sprintf("sni=%s", srv.SNI))
		}
	} else {
		params = append(params, "security=none")
	}

	// ALPN
	if len(srv.ALPN) > 0 {
		params = append(params, fmt.Sprintf("alpn=%s", strings.Join(srv.ALPN, ",")))
	}

	// WebSocket path
	if network == "ws" && srv.Path != "" {
		params = append(params, fmt.Sprintf("path=%s", srv.Path))
	}

	queryString := strings.Join(params, "&")
	name := srv.NodeName

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		srv.UUID, srv.NodeAddress, srv.Port, queryString, name)
}

// renderVMess generates a vmess:// URI (base64-encoded JSON).
func (r *V2RayRenderer) renderVMess(srv Server) (string, error) {
	vmess := map[string]interface{}{
		"v":    "2",
		"ps":   srv.NodeName,
		"add":  srv.NodeAddress,
		"port": srv.Port,
		"id":   srv.UUID,
		"aid":  0, // alterId, usually 0 for modern VMess
		"net":  srv.Network,
		"type": "none",
		"host": srv.SNI,
		"path": srv.Path,
		"tls":  "",
	}

	if srv.Network == "" {
		vmess["net"] = "tcp"
	}

	if srv.TLS {
		vmess["tls"] = "tls"
		if srv.SNI != "" {
			vmess["sni"] = srv.SNI
		}
	}

	jsonBytes, err := json.Marshal(vmess)
	if err != nil {
		return "", fmt.Errorf("marshal vmess json: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(jsonBytes)
	return "vmess://" + encoded, nil
}

// renderTrojan generates a trojan:// URI.
// Format: trojan://password@host:port?security=tls&sni=example.com#name
func (r *V2RayRenderer) renderTrojan(srv Server) string {
	var params []string

	if srv.TLS {
		params = append(params, "security=tls")
		if srv.SNI != "" {
			params = append(params, fmt.Sprintf("sni=%s", srv.SNI))
		}
	}

	if len(srv.ALPN) > 0 {
		params = append(params, fmt.Sprintf("alpn=%s", strings.Join(srv.ALPN, ",")))
	}

	queryString := strings.Join(params, "&")
	name := srv.NodeName

	if queryString != "" {
		return fmt.Sprintf("trojan://%s@%s:%d?%s#%s",
			srv.Password, srv.NodeAddress, srv.Port, queryString, name)
	}
	return fmt.Sprintf("trojan://%s@%s:%d#%s",
		srv.Password, srv.NodeAddress, srv.Port, name)
}
