package subscriptions

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
)

// Hysteria2 rendering for the three aggregated formats.
//
// It lives in one file rather than three because the protocol's fields are the
// same everywhere and only their spelling differs -- Clash wants "obfs-password",
// sing-box wants a nested object, and the URI wants a query parameter. Keeping
// them together is what stops one format quietly losing a field the other two
// carry.

// ErrNotRepresentable means this format has no way to express this protocol.
//
// Distinct from a malformed server: nothing is wrong, the format simply cannot
// say it. Callers skip these rather than failing the whole document, because a
// user with a VLESS inbound and a WireGuard inbound should still get their
// VLESS entry.
var ErrNotRepresentable = fmt.Errorf("protocol cannot be represented in this format")

// hysteria2URI builds a hy2:// link.
//
// The same scheme the per-inbound client configuration produces, so a user who
// imports the subscription and a user who scans the QR get identical settings
// rather than two subtly different ones.
func hysteria2URI(srv Server) (string, error) {
	if srv.Password == "" {
		// Refused rather than emitted with an empty credential: a link that
		// looks importable and cannot authenticate is worse than none.
		return "", fmt.Errorf("hysteria2 server %s has no password", srv.NodeName)
	}
	q := url.Values{}
	if srv.SNI != "" {
		q.Set("sni", srv.SNI)
	}
	if srv.Obfs != "" {
		q.Set("obfs", srv.Obfs)
		if srv.ObfsPassword != "" {
			q.Set("obfs-password", srv.ObfsPassword)
		}
	}

	uri := "hy2://" + url.QueryEscape(srv.Password) + "@" +
		net.JoinHostPort(srv.NodeAddress, strconv.Itoa(srv.Port))
	if len(q) > 0 {
		uri += "?" + q.Encode()
	}
	return uri + "#" + url.QueryEscape(srv.NodeName), nil
}

// clashHysteria2 builds a Clash Meta proxy entry.
//
// Clash's own hysteria2 support is Meta-only; the field names below are its
// spelling, not ours, and renaming them for consistency would produce a file
// Clash silently ignores.
func clashHysteria2(srv Server) (map[string]any, error) {
	if srv.Password == "" {
		return nil, fmt.Errorf("hysteria2 server %s has no password", srv.NodeName)
	}
	proxy := map[string]any{
		"name":     srv.NodeName,
		"type":     "hysteria2",
		"server":   srv.NodeAddress,
		"port":     srv.Port,
		"password": srv.Password,
		// Hysteria2 is always TLS; there is no plaintext mode to configure.
		"skip-cert-verify": false,
	}
	if srv.SNI != "" {
		proxy["sni"] = srv.SNI
	}
	if srv.Obfs != "" {
		proxy["obfs"] = srv.Obfs
		if srv.ObfsPassword != "" {
			proxy["obfs-password"] = srv.ObfsPassword
		}
	}
	// Bandwidth is a string with a unit in Clash, not a number. Emitting a
	// bare integer makes Clash reject the proxy.
	if srv.UpMbps > 0 {
		proxy["up"] = strconv.Itoa(srv.UpMbps) + " Mbps"
	}
	if srv.DownMbps > 0 {
		proxy["down"] = strconv.Itoa(srv.DownMbps) + " Mbps"
	}
	return proxy, nil
}

// singboxHysteria2 builds a sing-box outbound.
//
// sing-box nests obfs and tls as objects and takes bandwidth as bare integers,
// where Clash uses flat keys and strings. Same protocol, different document.
func singboxHysteria2(srv Server) (map[string]any, error) {
	if srv.Password == "" {
		return nil, fmt.Errorf("hysteria2 server %s has no password", srv.NodeName)
	}
	out := map[string]any{
		"type":        "hysteria2",
		"tag":         srv.NodeName,
		"server":      srv.NodeAddress,
		"server_port": srv.Port,
		"password":    srv.Password,
		"tls": map[string]any{
			"enabled": true,
			"server_name": func() string {
				if srv.SNI != "" {
					return srv.SNI
				}
				// No SNI configured: the address is what the certificate has
				// to match anyway, and omitting server_name makes sing-box
				// fall back to it. Stated rather than left blank.
				return srv.NodeAddress
			}(),
		},
	}
	if srv.Obfs != "" {
		obfs := map[string]any{"type": srv.Obfs}
		if srv.ObfsPassword != "" {
			obfs["password"] = srv.ObfsPassword
		}
		out["obfs"] = obfs
	}
	if srv.UpMbps > 0 {
		out["up_mbps"] = srv.UpMbps
	}
	if srv.DownMbps > 0 {
		out["down_mbps"] = srv.DownMbps
	}
	return out, nil
}
