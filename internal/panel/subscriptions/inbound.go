package subscriptions

import (
	"encoding/json"
	"strings"
)

// inboundParams is the subset of an adapter's service params that a
// subscription URI needs. The panel stores params opaquely; this is a
// read-only projection, not a second schema.
type inboundParams struct {
	Protocol    string
	Port        int
	Network     string
	Security    string
	SNI         string
	Path        string
	Host        string
	Dest        string
	ServerNames []string
	ShortIDs    []string
	PublicKey   string
	Flow        string
}

// ParseInbound projects opaque service params onto the fields a subscription URI needs.
func ParseInbound(raw []byte) InboundView {
	in := parseInboundParams(raw)
	return InboundView(in)
}

// InboundView is the exported shape of parseInboundParams.
type InboundView = inboundParams

func parseInboundParams(raw []byte) inboundParams {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return inboundParams{}
	}
	in := inboundParams{
		Protocol:  stringField(m, "protocol"),
		Port:      intField(m, "port"),
		Network:   stringField(m, "network"),
		Security:  stringField(m, "security"),
		SNI:       stringField(m, "sni"),
		Path:      stringField(m, "path"),
		Host:      stringField(m, "host"),
		Dest:      stringField(m, "dest"),
		PublicKey: firstNonEmpty(stringField(m, "public_key"), stringField(m, "pbk")),
		Flow:      stringField(m, "flow"),
	}
	in.ServerNames = stringSlice(m, "server_names")
	in.ShortIDs = stringSlice(m, "short_ids")
	if in.Protocol == "" {
		in.Protocol = "vless"
	}
	if in.Port == 0 {
		in.Port = 443
	}
	if in.Network == "" {
		in.Network = "tcp"
	}
	if in.Security == "" {
		in.Security = "none"
	}
	if in.SNI == "" && len(in.ServerNames) > 0 {
		in.SNI = in.ServerNames[0]
	}
	if in.Flow == "" && in.Protocol == "vless" && in.Network == "tcp" &&
		(in.Security == "tls" || in.Security == "reality") {
		in.Flow = "xtls-rprx-vision"
	}
	return in
}

func stringField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func intField(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func stringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
