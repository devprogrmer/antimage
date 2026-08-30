package ocserv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// File names inside the adapter's config directory.
const (
	confName   = "ocserv.conf"
	passwdName = "ocpasswd"
)

// markerPrefix identifies a file this adapter owns. A file without it is a
// human's, and the adapter refuses to overwrite it -- an operator who
// hand-configured ocserv before installing the agent keeps their config, and
// the drift is reported instead.
const markerPrefix = "# antimage-managed:"

// serviceParams is the validated shape of a service's params.
//
// Pointers where absence and zero differ. MaxClients=0 means "unlimited" to
// ocserv, so a plain int could not tell "the operator asked for unlimited"
// apart from "the operator said nothing", and the adapter would write a
// deliberate 0 either way.
type serviceParams struct {
	Port           int      `json:"port"`
	ServerCert     string   `json:"server_cert"`
	ServerKey      string   `json:"server_key"`
	IPv4Network    string   `json:"ipv4_network"`
	IPv4Netmask    string   `json:"ipv4_netmask"`
	DNS            []string `json:"dns"`
	Routes         []string `json:"routes"`
	MaxClients     *int     `json:"max_clients"`
	MaxSameClients *int     `json:"max_same_clients"`
	UDPEnabled     *bool    `json:"udp_enabled"`
	TunnelAllDNS   *bool    `json:"tunnel_all_dns"`
}

func parseServiceParams(raw json.RawMessage) (serviceParams, error) {
	var p serviceParams
	if len(raw) == 0 {
		return p, fmt.Errorf("ocserv service has no params")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	// The panel validates against serviceSchema, which sets
	// additionalProperties:false. Refusing unknown fields here too means a
	// params document that reached the node by any other route -- an older
	// panel, a hand-written test fixture -- fails loudly rather than being
	// applied with the unrecognised half dropped.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return serviceParams{}, fmt.Errorf("parse ocserv params: %w", err)
	}
	if p.Port <= 0 || p.Port > 65535 {
		return serviceParams{}, fmt.Errorf("ocserv port %d is out of range", p.Port)
	}
	if p.ServerCert == "" || p.ServerKey == "" {
		return serviceParams{}, fmt.Errorf("ocserv needs server_cert and server_key")
	}
	if p.IPv4Network == "" || p.IPv4Netmask == "" {
		return serviceParams{}, fmt.Errorf("ocserv needs ipv4_network and ipv4_netmask")
	}
	return p, nil
}

// usernameFor is the ocserv account name for a subject.
//
// Derived from the id rather than from the panel's subject name: ocserv
// usernames appear in occtl output and in the passwd file, and a name an
// operator can edit would break the mapping accounting depends on the moment
// somebody renamed a customer.
func usernameFor(subjectID int64) string {
	return fmt.Sprintf("subject-%d", subjectID)
}

// subjectIDFromUsername reverses usernameFor. Returns false for any account
// the adapter did not create, so a hand-added user is never mistaken for a
// subject and credited with somebody's traffic.
func subjectIDFromUsername(username string) (int64, bool) {
	var id int64
	if _, err := fmt.Sscanf(username, "subject-%d", &id); err != nil {
		return 0, false
	}
	// Sscanf accepts a numeric prefix, so "subject-12abc" would parse as 12.
	// Round-tripping rejects that.
	if usernameFor(id) != username {
		return 0, false
	}
	return id, true
}

// passwordFor returns the password credential for a subject, if it has one.
func passwordFor(s adapter.Subject) (string, bool) {
	for _, c := range s.Credentials {
		if c.Kind == string(adapter.CredPassword) && c.Value != "" {
			return c.Value, true
		}
	}
	return "", false
}

// desiredUsers is the account set one desired document implies, sorted so the
// checksum over it is stable.
//
// A subject with no password credential is skipped rather than created with an
// empty one: ocserv would accept an empty password as a valid login.
func desiredUsers(subjects []adapter.Subject) []userEntry {
	out := make([]userEntry, 0, len(subjects))
	for _, s := range subjects {
		pw, ok := passwordFor(s)
		if !ok {
			continue
		}
		out = append(out, userEntry{Name: usernameFor(s.ID), Password: pw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type userEntry struct {
	Name     string
	Password string
}

// renderConf produces ocserv.conf for one service.
//
// Deterministic: the same params always render byte-for-byte the same file, so
// the checksum in the marker is a reliable answer to "does this file match the
// desired state". Anything non-deterministic here would make every Plan see
// drift and rewrite the file forever.
func renderConf(serviceID int64, p serviceParams, passwdPath string) string {
	var b strings.Builder

	// The marker goes first so Observe can read it without parsing the file.
	// Checksum is filled in by the caller, which needs the rendered body.
	b.WriteString("auth = \"plain[passwd=" + passwdPath + "]\"\n")
	fmt.Fprintf(&b, "tcp-port = %d\n", p.Port)
	if p.UDPEnabled == nil || *p.UDPEnabled {
		// DTLS over UDP is what makes the connection usable on a lossy link;
		// TCP-only ocserv works and feels far worse. Default on.
		fmt.Fprintf(&b, "udp-port = %d\n", p.Port)
	}
	b.WriteString("server-cert = " + p.ServerCert + "\n")
	b.WriteString("server-key = " + p.ServerKey + "\n")

	b.WriteString("ipv4-network = " + p.IPv4Network + "\n")
	b.WriteString("ipv4-netmask = " + p.IPv4Netmask + "\n")

	// 0 means unlimited to ocserv, and the schema allows it deliberately.
	if p.MaxClients != nil {
		fmt.Fprintf(&b, "max-clients = %d\n", *p.MaxClients)
	}
	if p.MaxSameClients != nil {
		fmt.Fprintf(&b, "max-same-clients = %d\n", *p.MaxSameClients)
	}

	for _, d := range p.DNS {
		b.WriteString("dns = " + d + "\n")
	}
	// No routes means a default route, which is what a VPN is usually for.
	// Writing an empty route list instead would send nothing through it.
	for _, r := range p.Routes {
		b.WriteString("route = " + r + "\n")
	}
	if p.TunnelAllDNS != nil && *p.TunnelAllDNS {
		b.WriteString("tunnel-all-dns = true\n")
	}

	// Fixed operational settings. These are not exposed in the schema because
	// they are not choices an operator should have to make: the socket and pid
	// paths are what the packaged unit file expects, and isolation is a
	// security default nobody benefits from turning off.
	b.WriteString("socket-file = /run/ocserv-socket\n")
	b.WriteString("run-as-user = ocserv\n")
	b.WriteString("run-as-group = ocserv\n")
	b.WriteString("isolate-workers = true\n")
	b.WriteString("try-mtu-discovery = true\n")
	b.WriteString("cisco-client-compat = true\n")
	b.WriteString("dtls-legacy = true\n")

	body := b.String()
	return markerLine(serviceID, checksumOf(body)) + body
}

func markerLine(serviceID int64, checksum string) string {
	return fmt.Sprintf("%s service_id=%d checksum=%s\n", markerPrefix, serviceID, checksum)
}

// parseMarker extracts service id and checksum from a marker line.
func parseMarker(line string) (serviceID int64, checksum string, ok bool) {
	if !strings.HasPrefix(line, markerPrefix) {
		return 0, "", false
	}
	for _, part := range strings.Fields(strings.TrimPrefix(line, markerPrefix)) {
		switch {
		case strings.HasPrefix(part, "service_id="):
			_, _ = fmt.Sscanf(part, "service_id=%d", &serviceID)
		case strings.HasPrefix(part, "checksum="):
			checksum = strings.TrimPrefix(part, "checksum=")
		}
	}
	return serviceID, checksum, checksum != ""
}

func checksumOf(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// usersChecksum is the fingerprint of an account set.
//
// Over the NAMES only, not the passwords. The passwd file stores salted crypt
// hashes that ocpasswd regenerates with a fresh salt every write, so a
// password-sensitive checksum would differ from the file on every pass and
// report permanent drift. Names are what the file can actually be compared
// against -- see Observe.
func usersChecksum(users []userEntry) string {
	names := make([]string, 0, len(users))
	for _, u := range users {
		names = append(names, u.Name)
	}
	sort.Strings(names)
	return checksumOf(strings.Join(names, "\n"))
}

// serviceChecksum combines the two files into the one value the contract
// carries, JOINED rather than hashed together.
//
// Hashing the pair would produce a single opaque value, and Plan needs to know
// WHICH half changed: a config change costs a reload and a user change costs
// nothing, so collapsing them would charge every added user the price of a
// reload. ObservedService.Checksum is an opaque string as far as the contract
// is concerned, so carrying two fields in it is within the contract; splitting
// it is this package's business and nobody else's.
func serviceChecksum(confChecksum, usersChecksum string) string {
	return confChecksum + "." + usersChecksum
}

// splitServiceChecksum reverses serviceChecksum. The second return is false
// for a value this adapter did not write -- an older marker, or a checksum
// from before the format carried two halves -- which callers treat as "cannot
// tell what changed", i.e. rewrite both.
func splitServiceChecksum(combined string) (conf, users string, ok bool) {
	conf, users, ok = strings.Cut(combined, ".")
	if !ok || conf == "" || users == "" {
		return "", "", false
	}
	return conf, users, true
}
