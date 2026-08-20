package l2tp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

const (
	markerPrefix = "# antimage-managed:"
	markerXL2TPD = "; antimage-managed:" // xl2tpd uses semicolon comments

	ipsecConfPath   = "/etc/strongswan/ipsec.conf"
	ipsecSecretsPath = "/etc/strongswan/ipsec.secrets"
	xl2tpdConfPath  = "/etc/xl2tpd/xl2tpd.conf"
	chapSecretsPath = "/etc/ppp/chap-secrets"
	pppOptionsPath  = "/etc/ppp/options.xl2tpd"
)

// ServiceParams mirrors the JSON schema in adapter.go.
type ServiceParams struct {
	IPRange    string   `json:"ip_range"`
	LocalIP    string   `json:"local_ip"`
	PSK        string   `json:"psk"`
	DNSServers []string `json:"dns_servers"`
}

func parseServiceParams(raw json.RawMessage) (ServiceParams, error) {
	var p ServiceParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("parse service params: %w", err)
	}
	return p, nil
}

// renderIPsecConf generates strongSwan ipsec.conf.
// SP6 design decision 1: PSK authentication, not certificates.
func renderIPsecConf(serviceID int64, params ServiceParams) string {
	// Extract network from local_ip (assume /24)
	localNet := params.LocalIP + "/24"

	payload := fmt.Sprintf(`config setup
    charondebug="ike 1, knl 1, cfg 0"
    uniqueids=no

conn antimage-l2tp
    keyexchange=ikev2
    ike=aes256-sha256-modp2048!
    esp=aes256-sha256!
    left=%%any
    leftsubnet=0.0.0.0/0
    right=%%any
    rightsubnet=%s
    authby=secret
    type=transport
    auto=add
`, localNet)

	checksum := checksumOf(payload)
	return fmt.Sprintf("%s service_id=%d checksum=%s\n%s",
		markerPrefix, serviceID, checksum, payload)
}

// renderIPsecSecrets generates strongSwan ipsec.secrets with PSK.
func renderIPsecSecrets(serviceID int64, params ServiceParams) string {
	payload := fmt.Sprintf(`: PSK "%s"`, params.PSK) + "\n"

	checksum := checksumOf(payload)
	return fmt.Sprintf("%s service_id=%d checksum=%s\n%s",
		markerPrefix, serviceID, checksum, payload)
}

// renderXL2TPDConf generates xl2tpd.conf.
func renderXL2TPDConf(serviceID int64, params ServiceParams) string {
	payload := fmt.Sprintf(`[global]
port = 1701

[lns default]
ip range = %s
local ip = %s
require chap = yes
refuse pap = yes
require authentication = yes
name = antimage-l2tp
pppoptfile = /etc/ppp/options.xl2tpd
length bit = yes
`, params.IPRange, params.LocalIP)

	checksum := checksumOf(payload)
	return fmt.Sprintf("%s service_id=%d checksum=%s\n%s",
		markerXL2TPD, serviceID, checksum, payload)
}

// renderCHAPSecrets generates /etc/ppp/chap-secrets.
// SP6 design decision 2: username = sanitized subject name, password from credential.
func renderCHAPSecrets(serviceID int64, subjects []adapter.Subject) string {
	var lines []string
	for _, subj := range subjects {
		for _, cred := range subj.Credentials {
			if cred.Kind == string(adapter.CredPassword) {
				// Format: username * password *
				username := sanitizeUsername(subj.ID)
				lines = append(lines, fmt.Sprintf("%s\t*\t%s\t*", username, cred.Value))
			}
		}
	}

	// Sort for deterministic output (convergence property test requirement).
	sort.Strings(lines)

	payload := strings.Join(lines, "\n")
	if len(lines) > 0 {
		payload += "\n"
	}

	checksum := checksumOf(payload)
	return fmt.Sprintf("%s service_id=%d checksum=%s\n%s",
		markerPrefix, serviceID, checksum, payload)
}

// renderPPPOptions generates /etc/ppp/options.xl2tpd.
func renderPPPOptions(serviceID int64, params ServiceParams) string {
	var dnsLines strings.Builder
	for _, dns := range params.DNSServers {
		dnsLines.WriteString(fmt.Sprintf("ms-dns %s\n", dns))
	}

	payload := fmt.Sprintf(`require-mschap-v2
%snomppe
nodefaultroute
proxyarp
lcp-echo-interval 30
lcp-echo-failure 4
`, dnsLines.String())

	checksum := checksumOf(payload)
	return fmt.Sprintf("%s service_id=%d checksum=%s\n%s",
		markerPrefix, serviceID, checksum, payload)
}

// checksumOf computes SHA-256 of the payload (excluding marker line).
func checksumOf(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// sanitizeUsername converts subject ID to a PPP-safe username.
// PPP usernames must be alphanumeric + underscore + hyphen.
func sanitizeUsername(subjectID int64) string {
	return fmt.Sprintf("user%d", subjectID)
}

// isManaged reports whether a file's content starts with our marker.
func isManaged(content string) bool {
	return strings.HasPrefix(content, markerPrefix) ||
		strings.HasPrefix(content, markerXL2TPD)
}

// parseMarker extracts service_id and checksum from a marker line.
// Returns zero values if parsing fails.
func parseMarker(line string) (serviceID int64, checksum string, ok bool) {
	// Marker format: "# antimage-managed: service_id=123 checksum=abc..."
	// or: "; antimage-managed: service_id=123 checksum=abc..."
	line = strings.TrimPrefix(line, markerPrefix)
	line = strings.TrimPrefix(line, markerXL2TPD)
	line = strings.TrimSpace(line)

	parts := strings.Fields(line)
	for _, part := range parts {
		if strings.HasPrefix(part, "service_id=") {
			fmt.Sscanf(part, "service_id=%d", &serviceID)
		}
		if strings.HasPrefix(part, "checksum=") {
			checksum = strings.TrimPrefix(part, "checksum=")
		}
	}

	ok = serviceID > 0 && checksum != ""
	return
}

// extractPayload removes the marker line from file content.
func extractPayload(content string) string {
	lines := strings.SplitN(content, "\n", 2)
	if len(lines) < 2 {
		return ""
	}
	return lines[1]
}
