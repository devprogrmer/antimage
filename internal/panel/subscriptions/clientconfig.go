package subscriptions

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Delivery says how a client is expected to receive this protocol's
// configuration, which is not the same question for every protocol.
type Delivery string

const (
	// DeliveryURI is a single link a client imports, and the only kind a QR
	// code can carry.
	DeliveryURI Delivery = "uri"
	// DeliveryFile is a configuration document the user saves and opens with
	// their client. WireGuard and OpenVPN work this way; neither has a link
	// format, and inventing one would produce something no client can read.
	DeliveryFile Delivery = "file"
	// DeliveryManual is a protocol with no importable artefact at all. The
	// user types a server address, a username and a password into their
	// operating system's own VPN settings. L2TP and OpenConnect are this.
	DeliveryManual Delivery = "manual"
)

// ClientConfig is what one inbound gives one subject.
//
// The type exists because "generate a subscription" is not one operation. A
// VLESS inbound produces a link; a WireGuard inbound produces a file that has
// no link representation; an L2TP inbound produces neither and the user has to
// be told what to type. Collapsing those into one string is how a panel ends
// up handing somebody a vless:// URI for their WireGuard tunnel.
type ClientConfig struct {
	ServiceID   int64  `json:"service_id"`
	NodeID      int64  `json:"node_id"`
	NodeName    string `json:"node_name"`
	AdapterKind string `json:"adapter_kind"`
	// Protocol is the specific protocol inside the adapter where one applies
	// -- vless for an Xray inbound -- and the adapter kind otherwise.
	Protocol string   `json:"protocol"`
	Delivery Delivery `json:"delivery"`

	// URI is set only for DeliveryURI. It is what a QR code encodes.
	URI string `json:"uri,omitempty"`

	// FileName and FileBody are set only for DeliveryFile.
	FileName string `json:"file_name,omitempty"`
	FileBody string `json:"file_body,omitempty"`

	// Manual carries what the user must type for DeliveryManual.
	Manual *ManualConfig `json:"manual,omitempty"`

	// Note explains a limitation to the operator. Set when a protocol cannot
	// appear in an aggregated subscription format, so the UI can say why
	// rather than leaving a gap the operator has to guess at.
	Note string `json:"note,omitempty"`
}

// ManualConfig is what somebody types into their OS VPN settings.
type ManualConfig struct {
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	// PSK is the IPsec pre-shared key, which L2TP needs and OpenConnect does
	// not. Empty when the protocol has none.
	PSK string `json:"psk,omitempty"`
}

// Credentials are the subject's unsealed secrets. Only what a protocol needs
// is read; the rest is never touched.
type Credentials struct {
	UUID     string
	Password string
}

// NodeRef is the host an inbound listens on.
type NodeRef struct {
	ID      int64
	Name    string
	Address string
}

// Inbound is one service as the config builder needs it.
type Inbound struct {
	ServiceID   int64
	AdapterKind string
	Params      map[string]any
}

// ErrNoClientConfig means this adapter has no client-facing configuration at
// all -- a legitimate answer, not a failure.
var ErrNoClientConfig = fmt.Errorf("adapter produces no client configuration")

// BuildClientConfig turns one inbound into the configuration a client needs.
//
// Dispatch is on the ADAPTER KIND first, not on a "protocol" field. The field
// only exists for Xray and sing-box; every other adapter's params have no such
// key, and the code this replaces defaulted a missing one to "vless" -- so a
// WireGuard inbound was emitted as a vless:// link pointing at port 51820.
// That is worse than omitting it: it is a plausible-looking artefact that no
// client can use and that nobody can debug from the link alone.
func BuildClientConfig(
	in Inbound, node NodeRef, subjectName string, creds Credentials,
) (ClientConfig, error) {
	cfg := ClientConfig{
		ServiceID:   in.ServiceID,
		NodeID:      node.ID,
		NodeName:    node.Name,
		AdapterKind: in.AdapterKind,
		Protocol:    in.AdapterKind,
	}

	switch in.AdapterKind {
	case "xray", "singbox":
		return buildProxyURI(cfg, in, node, subjectName, creds)
	case "hysteria2":
		return buildHysteria2(cfg, in, node, creds)
	case "wireguard":
		return buildWireGuard(cfg, in, node, creds)
	case "openvpn":
		return buildOpenVPN(cfg, in, node, subjectName, creds)
	case "ocserv":
		return buildManual(cfg, in, node, subjectName, creds, "", 443)
	case "l2tp":
		return buildL2TP(cfg, in, node, subjectName, creds)
	default:
		return ClientConfig{}, ErrNoClientConfig
	}
}

func intParam(params map[string]any, key string) int {
	switch v := params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

func strParam(params map[string]any, key string) string {
	if s, ok := params[key].(string); ok {
		return s
	}
	return ""
}

func boolParam(params map[string]any, key string) bool {
	b, _ := params[key].(bool)
	return b
}

// hostPort joins an address and port, bracketing a bare IPv6 literal. Without
// the brackets a v6 address produces a URI that no client parses.
func hostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// buildProxyURI handles the Xray and sing-box family, where the specific
// protocol is a field inside the params.
func buildProxyURI(
	cfg ClientConfig, in Inbound, node NodeRef, subjectName string, creds Credentials,
) (ClientConfig, error) {
	proto := strParam(in.Params, "protocol")
	if proto == "" {
		// No default. An inbound whose params do not name a protocol is one
		// this builder does not understand, and guessing is what produced
		// invalid links before.
		return ClientConfig{}, fmt.Errorf(
			"%s inbound %d names no protocol", in.AdapterKind, in.ServiceID)
	}
	port := intParam(in.Params, "port")
	if port == 0 {
		return ClientConfig{}, fmt.Errorf("inbound %d names no port", in.ServiceID)
	}

	cfg.Protocol = proto
	cfg.Delivery = DeliveryURI

	// Read from the params rather than assumed. The code this replaces
	// hardcoded TLS=true and network=tcp, so a plaintext WebSocket inbound
	// produced a link claiming TLS over TCP -- which fails to connect and
	// gives no clue why.
	network := strParam(in.Params, "network")
	if network == "" {
		network = "tcp"
	}
	security := strParam(in.Params, "security")
	tls := security == "tls" || security == "reality" || boolParam(in.Params, "tls")

	q := url.Values{}
	q.Set("type", network)
	if tls {
		q.Set("security", security)
		if security == "" {
			q.Set("security", "tls")
		}
	} else {
		q.Set("security", "none")
	}
	if sni := strParam(in.Params, "sni"); sni != "" {
		q.Set("sni", sni)
	}
	if host := strParam(in.Params, "host"); host != "" {
		q.Set("host", host)
	}
	if path := strParam(in.Params, "path"); path != "" {
		q.Set("path", path)
	}
	if sid := strParam(in.Params, "service_name"); sid != "" {
		q.Set("serviceName", sid)
	}
	// REALITY carries its own public key and short id; a link missing them
	// cannot complete the handshake.
	if pbk := strParam(in.Params, "public_key"); pbk != "" {
		q.Set("pbk", pbk)
	}
	if sid := strParam(in.Params, "short_id"); sid != "" {
		q.Set("sid", sid)
	}
	if flow := strParam(in.Params, "flow"); flow != "" {
		q.Set("flow", flow)
	}

	label := url.QueryEscape(fmt.Sprintf("%s-%s", node.Name, proto))
	addr := hostPort(node.Address, port)

	switch proto {
	case "vless":
		if creds.UUID == "" {
			return ClientConfig{}, fmt.Errorf("vless inbound %d has no uuid for this subject", in.ServiceID)
		}
		cfg.URI = fmt.Sprintf("vless://%s@%s?%s#%s", creds.UUID, addr, q.Encode(), label)

	case "vmess":
		if creds.UUID == "" {
			return ClientConfig{}, fmt.Errorf("vmess inbound %d has no uuid for this subject", in.ServiceID)
		}
		// VMess is a base64 JSON blob rather than a query string. Its field
		// names are fixed by the format and are not ours to tidy.
		blob := map[string]any{
			"v": "2", "ps": fmt.Sprintf("%s-vmess", node.Name),
			"add": node.Address, "port": strconv.Itoa(port),
			"id": creds.UUID, "aid": "0", "scy": "auto",
			"net": network, "type": "none",
			"host": strParam(in.Params, "host"),
			"path": strParam(in.Params, "path"),
			"tls":  map[bool]string{true: "tls", false: ""}[tls],
			"sni":  strParam(in.Params, "sni"),
		}
		body, err := json.Marshal(blob)
		if err != nil {
			return ClientConfig{}, err
		}
		cfg.URI = "vmess://" + base64.StdEncoding.EncodeToString(body)

	case "trojan":
		if creds.Password == "" {
			return ClientConfig{}, fmt.Errorf("trojan inbound %d has no password for this subject", in.ServiceID)
		}
		cfg.URI = fmt.Sprintf("trojan://%s@%s?%s#%s",
			url.QueryEscape(creds.Password), addr, q.Encode(), label)

	case "shadowsocks":
		if creds.Password == "" {
			return ClientConfig{}, fmt.Errorf("shadowsocks inbound %d has no password for this subject", in.ServiceID)
		}
		method := strParam(in.Params, "method")
		if method == "" {
			method = "aes-256-gcm"
		}
		// SIP002: base64url(method:password), no padding.
		userinfo := base64.RawURLEncoding.EncodeToString(
			[]byte(method + ":" + creds.Password))
		cfg.URI = fmt.Sprintf("ss://%s@%s#%s", userinfo, addr, label)

	default:
		return ClientConfig{}, fmt.Errorf(
			"protocol %q on inbound %d has no client link format here", proto, in.ServiceID)
	}
	return cfg, nil
}

// buildHysteria2 produces a hy2:// link, which the protocol does define.
func buildHysteria2(
	cfg ClientConfig, in Inbound, node NodeRef, creds Credentials,
) (ClientConfig, error) {
	port := intParam(in.Params, "port")
	if port == 0 {
		return ClientConfig{}, fmt.Errorf("hysteria2 inbound %d names no port", in.ServiceID)
	}
	// The inbound's own password is shared by every user of that inbound --
	// it is a server setting, not a per-subject credential -- so the subject's
	// password is preferred when it has one.
	pw := creds.Password
	if pw == "" {
		pw = strParam(in.Params, "password")
	}
	if pw == "" {
		return ClientConfig{}, fmt.Errorf("hysteria2 inbound %d has no password", in.ServiceID)
	}

	q := url.Values{}
	if sni := strParam(in.Params, "sni"); sni != "" {
		q.Set("sni", sni)
	}
	if obfs := strParam(in.Params, "obfs"); obfs != "" {
		q.Set("obfs", obfs)
		if op := strParam(in.Params, "obfs_password"); op != "" {
			q.Set("obfs-password", op)
		}
	}

	cfg.Protocol = "hysteria2"
	cfg.Delivery = DeliveryURI
	uri := fmt.Sprintf("hy2://%s@%s", url.QueryEscape(pw), hostPort(node.Address, port))
	if len(q) > 0 {
		uri += "?" + q.Encode()
	}
	cfg.URI = uri + "#" + url.QueryEscape(node.Name)
	return cfg, nil
}

// buildWireGuard produces a native .conf.
//
// WireGuard has NO link format. Emitting a URI for it -- which is what the
// previous code did by defaulting the protocol to vless -- produces something
// that looks importable and is not.
func buildWireGuard(
	cfg ClientConfig, in Inbound, node NodeRef, creds Credentials,
) (ClientConfig, error) {
	port := intParam(in.Params, "port")
	if port == 0 {
		return ClientConfig{}, fmt.Errorf("wireguard inbound %d names no port", in.ServiceID)
	}

	var b strings.Builder
	b.WriteString("[Interface]\n")
	// The client's own private key is not something the panel holds: WireGuard
	// keys are generated by the peer. The placeholder is explicit rather than
	// silently absent, so the user knows the one thing they must supply.
	b.WriteString("PrivateKey = <your client private key>\n")
	if addr := strParam(in.Params, "client_address"); addr != "" {
		b.WriteString("Address = " + addr + "\n")
	}
	if dns, ok := in.Params["dns"].([]any); ok && len(dns) > 0 {
		var servers []string
		for _, d := range dns {
			if s, ok := d.(string); ok {
				servers = append(servers, s)
			}
		}
		if len(servers) > 0 {
			b.WriteString("DNS = " + strings.Join(servers, ", ") + "\n")
		}
	}
	if mtu := intParam(in.Params, "mtu"); mtu > 0 {
		b.WriteString("MTU = " + strconv.Itoa(mtu) + "\n")
	}

	b.WriteString("\n[Peer]\n")
	// The SERVER's public key. The adapter stores a private key; the public
	// half is what a client needs, and if the panel does not have it the file
	// says so rather than shipping an empty field that fails silently.
	pub := strParam(in.Params, "public_key")
	if pub == "" {
		pub = "<server public key -- not recorded by the panel>"
		cfg.Note = "noServerPublicKey"
	}
	b.WriteString("PublicKey = " + pub + "\n")
	b.WriteString("Endpoint = " + hostPort(node.Address, port) + "\n")
	b.WriteString("AllowedIPs = 0.0.0.0/0, ::/0\n")
	b.WriteString("PersistentKeepalive = 25\n")

	cfg.Protocol = "wireguard"
	cfg.Delivery = DeliveryFile
	cfg.FileName = fmt.Sprintf("%s-wg.conf", sanitizeFileName(node.Name))
	cfg.FileBody = b.String()
	return cfg, nil
}

// buildOpenVPN produces a native .ovpn.
//
// The adapter authenticates with a username and password rather than a client
// certificate (see the openvpn adapter's package comment), so the profile
// carries auth-user-pass and the CA the operator configured.
func buildOpenVPN(
	cfg ClientConfig, in Inbound, node NodeRef, subjectName string, creds Credentials,
) (ClientConfig, error) {
	port := intParam(in.Params, "port")
	if port == 0 {
		return ClientConfig{}, fmt.Errorf("openvpn inbound %d names no port", in.ServiceID)
	}
	proto := strParam(in.Params, "proto")
	if proto == "" {
		proto = "udp"
	}

	var b strings.Builder
	b.WriteString("client\n")
	b.WriteString("dev tun\n")
	b.WriteString("proto " + proto + "\n")
	fmt.Fprintf(&b, "remote %s %d\n", node.Address, port)
	b.WriteString("resolv-retry infinite\n")
	b.WriteString("nobind\n")
	b.WriteString("persist-key\n")
	b.WriteString("persist-tun\n")
	b.WriteString("remote-cert-tls server\n")
	b.WriteString("auth-user-pass\n")
	if cipher := strParam(in.Params, "cipher"); cipher != "" {
		b.WriteString("data-ciphers " + cipher + "\n")
	}
	b.WriteString("verb 3\n")
	// The CA is a path on the SERVER, not content the panel holds. Saying so
	// beats emitting an empty <ca> block that OpenVPN rejects at connect time
	// with an error nobody can trace back to here.
	b.WriteString("\n# Add the server's CA certificate below as:\n")
	b.WriteString("# <ca>\n# -----BEGIN CERTIFICATE-----\n# ...\n# </ca>\n")
	cfg.Note = "openvpnNeedsCA"

	cfg.Protocol = "openvpn"
	cfg.Delivery = DeliveryFile
	cfg.FileName = fmt.Sprintf("%s-%s.ovpn", sanitizeFileName(node.Name), sanitizeFileName(subjectName))
	cfg.FileBody = b.String()
	return cfg, nil
}

// buildL2TP is manual, and additionally carries the IPsec pre-shared key.
func buildL2TP(
	cfg ClientConfig, in Inbound, node NodeRef, subjectName string, creds Credentials,
) (ClientConfig, error) {
	out, err := buildManual(cfg, in, node, subjectName, creds, strParam(in.Params, "psk"), 1701)
	if err != nil {
		return out, err
	}
	out.Protocol = "l2tp"
	return out, nil
}

// buildManual covers the protocols a user configures in their operating
// system's own VPN settings. There is no file and no link to generate.
func buildManual(
	cfg ClientConfig, in Inbound, node NodeRef, subjectName string,
	creds Credentials, psk string, defaultPort int,
) (ClientConfig, error) {
	port := intParam(in.Params, "port")
	if port == 0 {
		port = defaultPort
	}
	cfg.Delivery = DeliveryManual
	cfg.Manual = &ManualConfig{
		Server:   node.Address,
		Port:     port,
		Username: subjectName,
		Password: creds.Password,
		PSK:      psk,
	}
	// Named so the UI can say which formats this protocol cannot appear in,
	// rather than leaving a hole in the subscription the operator has to
	// explain to a customer.
	cfg.Note = "notInAggregatedFormats"
	return cfg, nil
}

// sanitizeFileName keeps a node or subject name usable as a download name.
func sanitizeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "config"
	}
	return out
}

// AggregatableURIs returns the subset of configs that an aggregated
// subscription format (V2Ray, Clash, sing-box) can actually carry.
//
// Everything else is deliberately excluded rather than approximated. A Clash
// file containing a fabricated entry for a WireGuard tunnel is worse than one
// that omits it: the omission is visible, the fabrication is not.
func AggregatableURIs(configs []ClientConfig) []string {
	var out []string
	for _, c := range configs {
		if c.Delivery == DeliveryURI && c.URI != "" {
			out = append(out, c.URI)
		}
	}
	sort.Strings(out)
	return out
}

// ServerFromInbound maps one inbound onto the Server the aggregated renderers
// take, or reports that no aggregated format can carry it.
//
// The same dispatch BuildClientConfig uses, deliberately. The subscription
// endpoint and the per-inbound panel have to agree about what a protocol is:
// two independent readings of the same params is how one of them ends up
// emitting a vless:// link for a WireGuard tunnel while the other does not.
func ServerFromInbound(
	in Inbound, node NodeRef, creds Credentials,
) (Server, error) {
	srv := Server{
		NodeID:      node.ID,
		NodeName:    node.Name,
		NodeAddress: node.Address,
		ServiceID:   in.ServiceID,
		UUID:        creds.UUID,
		Password:    creds.Password,
	}

	switch in.AdapterKind {
	case "xray", "singbox":
		proto := strParam(in.Params, "protocol")
		if proto == "" {
			return Server{}, fmt.Errorf("%s inbound %d names no protocol",
				in.AdapterKind, in.ServiceID)
		}
		srv.Protocol = proto
		srv.Port = intParam(in.Params, "port")
		srv.Network = strParam(in.Params, "network")
		if srv.Network == "" {
			srv.Network = "tcp"
		}
		security := strParam(in.Params, "security")
		// Read, not assumed. gatherServers hardcoded TLS true and network tcp,
		// so a plaintext WebSocket inbound produced a subscription entry
		// claiming TLS over TCP that could not connect.
		srv.TLS = security == "tls" || security == "reality" || boolParam(in.Params, "tls")
		srv.SNI = strParam(in.Params, "sni")
		srv.Path = strParam(in.Params, "path")
		// Shadowsocks carries its own cipher; without it the client decrypts
		// with the wrong algorithm and the tunnel fails after connecting.
		srv.Method = strParam(in.Params, "method")

	case "hysteria2":
		srv.Protocol = "hysteria2"
		srv.Port = intParam(in.Params, "port")
		srv.SNI = strParam(in.Params, "sni")
		srv.Obfs = strParam(in.Params, "obfs")
		srv.ObfsPassword = strParam(in.Params, "obfs_password")
		srv.UpMbps = intParam(in.Params, "up_mbps")
		srv.DownMbps = intParam(in.Params, "down_mbps")
		// The inbound's own password is shared by every user of it; the
		// subject's is preferred where they have one.
		if srv.Password == "" {
			srv.Password = strParam(in.Params, "password")
		}

	default:
		// WireGuard, OpenVPN, ocserv and L2TP have no representation in any
		// aggregated subscription format. Reported as such so the caller skips
		// them rather than inventing an entry.
		return Server{}, ErrNotRepresentable
	}

	if srv.Port == 0 {
		return Server{}, fmt.Errorf("inbound %d names no port", in.ServiceID)
	}
	return srv, nil
}
