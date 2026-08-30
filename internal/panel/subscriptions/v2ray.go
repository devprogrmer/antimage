package subscriptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	// Hysteria2. Obfs is the obfuscation mode ("salamander" or empty), and
	// ObfsPassword is meaningless without it. UpMbps and DownMbps are the
	// congestion-control hints the protocol needs to pace itself; zero means
	// the client picks, which is a legitimate configuration rather than a
	// missing one.
	// Method is the Shadowsocks cipher. Clash calls this field "cipher" and
	// sing-box calls it "method"; the name here follows the protocol's own
	// terminology rather than either client's.
	Method       string
	Obfs         string
	ObfsPassword string
	UpMbps       int
	DownMbps     int
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
		lines = append(lines, uri)
	}

	if len(lines) == 0 {
		return nil, "", fmt.Errorf(
			"none of this subject's inbounds can be expressed in a v2ray subscription")
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
	case "hysteria2":
		return hysteria2URI(srv)
	case "shadowsocks":
		return shadowsocksURI(srv)
	default:
		// Not an error: this format cannot express the protocol. The caller
		// skips it so one WireGuard inbound does not empty the whole
		// subscription.
		return "", ErrNotRepresentable
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
