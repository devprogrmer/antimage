package subscriptions

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// Shadowsocks rendering for the three aggregated formats.
//
// Kept in one file for the same reason hysteria2 is: the protocol's fields are
// identical everywhere and only their spelling differs. Clash calls the cipher
// "cipher" and the proxy type "ss"; sing-box calls them "method" and
// "shadowsocks". Splitting them across three files is how one format quietly
// loses a field the other two carry.

// defaultMethod is what an inbound that names no cipher is assumed to use.
//
// aes-256-gcm because that is what the sing-box adapter's schema lists first
// and what every current client supports. A wrong guess here does not produce
// a broken-looking config -- it produces one that connects and then fails to
// decrypt, which is far harder to diagnose -- so callers that KNOW the cipher
// must pass it rather than relying on this.
const defaultMethod = "aes-256-gcm"

func methodOr(m string) string {
	if m == "" {
		return defaultMethod
	}
	return m
}

// shadowsocksURI builds a SIP002 ss:// link.
//
// SIP002 encodes method:password as base64url WITHOUT padding in the userinfo
// position. The older SIP001 form base64-encoded the whole userinfo@host:port
// and is no longer accepted by current clients, so getting this wrong produces
// a link that is silently refused rather than one that errors visibly.
func shadowsocksURI(srv Server) (string, error) {
	if srv.Password == "" {
		return "", fmt.Errorf("shadowsocks server %s has no password", srv.NodeName)
	}
	userinfo := base64.RawURLEncoding.EncodeToString(
		[]byte(methodOr(srv.Method) + ":" + srv.Password))
	return fmt.Sprintf("ss://%s@%s#%s",
		userinfo,
		net.JoinHostPort(srv.NodeAddress, strconv.Itoa(srv.Port)),
		url.QueryEscape(srv.NodeName)), nil
}

// clashShadowsocks builds a Clash proxy entry.
//
// The type is "ss", not "shadowsocks", and the cipher field is "cipher". Both
// are Clash's spelling; using the protocol's own names here produces a proxy
// Clash silently ignores.
func clashShadowsocks(srv Server) (map[string]any, error) {
	if srv.Password == "" {
		return nil, fmt.Errorf("shadowsocks server %s has no password", srv.NodeName)
	}
	return map[string]any{
		"name":     srv.NodeName,
		"type":     "ss",
		"server":   srv.NodeAddress,
		"port":     srv.Port,
		"cipher":   methodOr(srv.Method),
		"password": srv.Password,
		// Shadowsocks relays UDP, and Clash defaults it off. Leaving it off
		// breaks DNS and every UDP-based application through the tunnel.
		"udp": true,
	}, nil
}

// singboxShadowsocks builds a sing-box outbound.
//
// No TLS block: Shadowsocks carries its own encryption and has no TLS layer to
// configure. Adding an empty one -- as the vless and trojan renderers here do,
// where it belongs -- would make sing-box reject the outbound.
func singboxShadowsocks(srv Server) (map[string]any, error) {
	if srv.Password == "" {
		return nil, fmt.Errorf("shadowsocks server %s has no password", srv.NodeName)
	}
	return map[string]any{
		"type":        "shadowsocks",
		"tag":         srv.NodeName,
		"server":      srv.NodeAddress,
		"server_port": srv.Port,
		"method":      methodOr(srv.Method),
		"password":    srv.Password,
	}, nil
}
