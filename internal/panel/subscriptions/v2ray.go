package subscriptions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Server represents one node+credential pair to render in subscription configs.
type Server struct {
	NodeID        int64
	NodeName      string
	NodeAddress   string
	ServiceID     int64
	Protocol      string // "vless", "vmess", "trojan"
	Port          int
	UUID          string
	Password      string
	TLS           bool
	Security      string // "none", "tls", "reality"
	SNI           string
	ALPN          []string
	Network       string // "tcp", "ws", "grpc"
	Path          string
	Host          string
	Remark        string
	Fingerprint   string
	PublicKey     string
	ShortID       string
	SpiderX       string
	Flow          string
	AllowInsecure bool
}

// Label is the name shown in a client. A host remark wins over the node name.
func (s Server) Label() string {
	if strings.TrimSpace(s.Remark) != "" {
		return s.Remark
	}
	return s.NodeName
}

func (s Server) security() string {
	if s.Security != "" {
		return s.Security
	}
	if s.TLS {
		return "tls"
	}
	return "none"
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
			return nil, "", fmt.Errorf("render server %s: %w", srv.Label(), err)
		}
		lines = append(lines, uri)
	}

	content := strings.Join(lines, "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(content))

	return []byte(encoded), "text/plain; charset=utf-8", nil
}

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

func (r *V2RayRenderer) renderVLESS(srv Server) string {
	var params []string
	network := srv.Network
	if network == "" {
		network = "tcp"
	}
	params = append(params, "type="+network)
	params = append(params, "encryption=none")

	sec := srv.security()
	params = append(params, "security="+sec)
	if srv.SNI != "" {
		params = append(params, "sni="+srv.SNI)
	}
	if srv.Flow != "" {
		params = append(params, "flow="+srv.Flow)
	}
	if len(srv.ALPN) > 0 {
		params = append(params, "alpn="+strings.Join(srv.ALPN, ","))
	}
	if srv.Fingerprint != "" {
		params = append(params, "fp="+srv.Fingerprint)
	} else if sec == "reality" {
		params = append(params, "fp=chrome")
	}
	if sec == "reality" {
		if srv.PublicKey != "" {
			params = append(params, "pbk="+srv.PublicKey)
		}
		if srv.ShortID != "" {
			params = append(params, "sid="+srv.ShortID)
		}
		if srv.SpiderX != "" {
			params = append(params, "spx="+url.QueryEscape(srv.SpiderX))
		}
	}
	if network == "ws" {
		if srv.Path != "" {
			params = append(params, "path="+srv.Path)
		}
		if srv.Host != "" {
			params = append(params, "host="+srv.Host)
		}
	}
	if network == "grpc" && srv.Path != "" {
		params = append(params, "serviceName="+strings.TrimPrefix(srv.Path, "/"))
	}
	if srv.AllowInsecure {
		params = append(params, "allowInsecure=1")
	}

	return fmt.Sprintf("vless://%s@%s:%d?%s#%s",
		srv.UUID, srv.NodeAddress, srv.Port, strings.Join(params, "&"), srv.Label())
}

func (r *V2RayRenderer) renderVMess(srv Server) (string, error) {
	network := srv.Network
	if network == "" {
		network = "tcp"
	}
	vmess := map[string]interface{}{
		"v":    "2",
		"ps":   srv.Label(),
		"add":  srv.NodeAddress,
		"port": srv.Port,
		"id":   srv.UUID,
		"aid":  0,
		"net":  network,
		"type": "none",
		"host": srv.Host,
		"path": srv.Path,
		"tls":  "",
	}
	if srv.SNI != "" && vmess["host"] == "" {
		vmess["host"] = srv.SNI
	}
	sec := srv.security()
	if sec == "tls" || sec == "reality" {
		vmess["tls"] = "tls"
		if srv.SNI != "" {
			vmess["sni"] = srv.SNI
		}
	}
	if srv.Fingerprint != "" {
		vmess["fp"] = srv.Fingerprint
	}

	jsonBytes, err := json.Marshal(vmess)
	if err != nil {
		return "", fmt.Errorf("marshal vmess json: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(jsonBytes)
	return "vmess://" + encoded, nil
}

func (r *V2RayRenderer) renderTrojan(srv Server) string {
	var params []string
	sec := srv.security()
	if sec == "none" {
		sec = "tls"
	}
	params = append(params, "security="+sec)
	if srv.SNI != "" {
		params = append(params, "sni="+srv.SNI)
	}
	if len(srv.ALPN) > 0 {
		params = append(params, "alpn="+strings.Join(srv.ALPN, ","))
	}
	if srv.AllowInsecure {
		params = append(params, "allowInsecure=1")
	}
	network := srv.Network
	if network != "" && network != "tcp" {
		params = append(params, "type="+network)
		if srv.Path != "" {
			params = append(params, "path="+srv.Path)
		}
	}
	queryString := strings.Join(params, "&")
	name := srv.Label()
	if queryString != "" {
		return fmt.Sprintf("trojan://%s@%s:%d?%s#%s",
			srv.Password, srv.NodeAddress, srv.Port, queryString, name)
	}
	return fmt.Sprintf("trojan://%s@%s:%d#%s",
		srv.Password, srv.NodeAddress, srv.Port, name)
}
