package openvpn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Files inside the adapter's config directory.
const (
	confName   = "antimage-server.conf"
	verifyName = "antimage-verify.sh"
	usersName  = "antimage-users"
	statusName = "antimage-status.log"
)

// markerPrefix identifies a file this adapter owns. A file without it belongs
// to whoever wrote it, and is reported as drift rather than overwritten.
const markerPrefix = "# antimage-managed:"

type serviceParams struct {
	Port        int      `json:"port"`
	Proto       string   `json:"proto"`
	CA          string   `json:"ca"`
	ServerCert  string   `json:"server_cert"`
	ServerKey   string   `json:"server_key"`
	DH          string   `json:"dh"`
	Subnet      string   `json:"subnet"`
	Netmask     string   `json:"netmask"`
	DNS         []string `json:"dns"`
	Routes      []string `json:"routes"`
	Cipher      string   `json:"cipher"`
	MaxClients  *int     `json:"max_clients"`
	DuplicateCN *bool    `json:"duplicate_cn"`
}

func parseServiceParams(raw json.RawMessage) (serviceParams, error) {
	var p serviceParams
	if len(raw) == 0 {
		return p, fmt.Errorf("openvpn service has no params")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return serviceParams{}, fmt.Errorf("parse openvpn params: %w", err)
	}
	if p.Port <= 0 || p.Port > 65535 {
		return serviceParams{}, fmt.Errorf("openvpn port %d is out of range", p.Port)
	}
	if p.Proto != "udp" && p.Proto != "tcp" {
		return serviceParams{}, fmt.Errorf("openvpn proto %q must be udp or tcp", p.Proto)
	}
	for name, v := range map[string]string{
		"ca": p.CA, "server_cert": p.ServerCert, "server_key": p.ServerKey,
		"dh": p.DH, "subnet": p.Subnet, "netmask": p.Netmask,
	} {
		if v == "" {
			return serviceParams{}, fmt.Errorf("openvpn needs %s", name)
		}
	}
	// A newline in a path would inject a directive into server.conf. These
	// values reach here through the panel's schema validation, but the adapter
	// is what actually writes the file and cannot assume its caller checked.
	for name, v := range map[string]string{
		"ca": p.CA, "server_cert": p.ServerCert, "server_key": p.ServerKey, "dh": p.DH,
	} {
		if strings.ContainsAny(v, "\n\r") {
			return serviceParams{}, fmt.Errorf("openvpn %s must not contain a line break", name)
		}
	}
	return p, nil
}

// usernameFor is the OpenVPN account name for a subject. Derived from the id,
// not the operator-editable name, because it is also the accounting key: the
// status file reports traffic by common name.
func usernameFor(subjectID int64) string {
	return fmt.Sprintf("subject-%d", subjectID)
}

func subjectIDFromUsername(username string) (int64, bool) {
	var id int64
	if _, err := fmt.Sscanf(username, "subject-%d", &id); err != nil {
		return 0, false
	}
	if usernameFor(id) != username {
		return 0, false
	}
	return id, true
}

type userEntry struct {
	Name     string
	Password string
}

func desiredUsers(subjects []adapter.Subject) []userEntry {
	out := make([]userEntry, 0, len(subjects))
	for _, s := range subjects {
		var pw string
		for _, c := range s.Credentials {
			if c.Kind == string(adapter.CredPassword) && c.Value != "" {
				pw = c.Value
				break
			}
		}
		// No password means no account. Writing one with an empty password
		// would create a login that anybody can use.
		if pw == "" {
			continue
		}
		out = append(out, userEntry{Name: usernameFor(s.ID), Password: pw})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// saltFor derives a per-user salt.
//
// Deterministic on purpose: a random salt would make the user file differ on
// every render, and drift detection here is exact precisely because the
// adapter -- not a third-party tool -- writes every byte. A salt is not a
// secret; its job is to stop one precomputed table covering every user on
// every installation, and a value unique per service and per account does
// that.
func saltFor(serviceID int64, username string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("antimage-openvpn:%d:%s", serviceID, username)))
	return hex.EncodeToString(sum[:16])
}

// hashPassword is the digest the verify script recomputes at login.
//
// A SINGLE SHA-256 ROUND, which is weaker than a password hash should be, and
// the reason is worth stating rather than hiding: the verifier is a shell
// script that OpenVPN execs per login, and shell has no PBKDF2 or argon2. An
// iterated construction would mean thousands of forks per authentication.
//
// What actually protects these digests is that the file is 0600 and owned by
// root: an attacker who can read it already has root on the VPN server and
// does not need anyone's password. The hash defends against the file leaking
// through a backup or a misdirected copy, and a salted digest is enough for
// that. It is NOT enough if the file becomes readable, which is why the mode
// is asserted in a test rather than left to convention.
func hashPassword(serviceID int64, username, password string) (salt, digest string) {
	salt = saltFor(serviceID, username)
	sum := sha256.Sum256([]byte(salt + password))
	return salt, hex.EncodeToString(sum[:])
}

// renderUsers produces the account file the verify script reads.
//
// Format is "username:salt:digest", one per line, sorted. Fully deterministic,
// so the checksum over it is an exact answer to "does this match desired".
func renderUsers(serviceID int64, users []userEntry) string {
	var b strings.Builder
	for _, u := range users {
		salt, digest := hashPassword(serviceID, u.Name, u.Password)
		b.WriteString(u.Name + ":" + salt + ":" + digest + "\n")
	}
	body := b.String()
	return markerLine(serviceID, checksumOf(body)) + body
}

// renderVerify produces the script OpenVPN runs on each login attempt.
//
// SECURITY NOTES, because this script runs as root on every authentication:
//
//   - The username arrives from the CLIENT and is never interpolated into a
//     shell word. It is passed to awk through -v, which takes it as data.
//   - It is also pattern-checked against subject-N before use, so a name this
//     adapter never issued is rejected before it reaches the lookup.
//   - The password reaches only printf's argument list, never the command
//     string.
//   - via-file, not via-env: with via-env the password sits in the
//     environment of a process every local user can see in /proc.
func renderVerify(serviceID int64, usersPath string) string {
	body := `#!/bin/sh
# Verifies one OpenVPN login. OpenVPN passes a temp file whose first line is
# the username and whose second is the password. Exit 0 accepts, non-zero
# rejects.
set -eu

creds="$1"
[ -r "$creds" ] || exit 1

user=$(sed -n '1p' "$creds")
pass=$(sed -n '2p' "$creds")

# Reject anything this panel did not issue before it reaches the lookup.
case "$user" in
  subject-[0-9]*) ;;
  *) exit 1 ;;
esac

# -v passes the untrusted name to awk as DATA, never as part of a shell word.
row=$(awk -F: -v u="$user" '$1 == u { print $2 ":" $3; exit }' ` + shellQuote(usersPath) + `)
[ -n "$row" ] || exit 1

salt=${row%%:*}
want=${row#*:}
got=$(printf '%s' "$salt$pass" | sha256sum | cut -d' ' -f1)

[ "$got" = "$want" ] || exit 1
exit 0
`
	return markerLine(serviceID, checksumOf(body)) + body
}

// shellQuote wraps a path in single quotes for safe inclusion in the script.
// Paths come from operator-supplied params, and a quote in one would otherwise
// end the string and let the rest be read as script.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// renderConf produces server.conf. Deterministic: the same params always
// render identical bytes, which is what makes the checksum meaningful.
func renderConf(serviceID int64, p serviceParams, dir string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "port %d\n", p.Port)
	b.WriteString("proto " + p.Proto + "\n")
	// tun, not tap: routing rather than bridging, which is what every client
	// in this product expects.
	b.WriteString("dev tun\n")

	// OpenVPN 2.6+ no longer infers TLS mode from `server`; without an
	// explicit tls-server declaration the daemon falls back to static-key
	// mode and rejects the config asking for --secret. `client-config-dir`
	// alone would also imply tls-server, but stating it is clearer than
	// depending on that.
	b.WriteString("tls-server\n")
	b.WriteString("ca " + p.CA + "\n")
	b.WriteString("cert " + p.ServerCert + "\n")
	b.WriteString("key " + p.ServerKey + "\n")
	if p.DH == "none" {
		b.WriteString("dh none\n")
	} else {
		b.WriteString("dh " + p.DH + "\n")
	}

	fmt.Fprintf(&b, "server %s %s\n", p.Subnet, p.Netmask)
	b.WriteString("topology subnet\n")

	// The authentication decision, in three directives. Without
	// verify-client-cert none, OpenVPN still demands a client certificate and
	// every subject would fail to connect; without username-as-common-name the
	// status file reports every session under the server's CN and accounting
	// cannot tell customers apart.
	b.WriteString("verify-client-cert none\n")
	b.WriteString("username-as-common-name\n")
	b.WriteString("script-security 2\n")
	b.WriteString("auth-user-pass-verify " + dir + "/" + verifyName + " via-file\n")

	// Status file, version 2: the format the accounting parser reads. Version 1
	// omits the byte counters entirely.
	b.WriteString("status " + dir + "/" + statusName + " 10\n")
	b.WriteString("status-version 2\n")

	if p.Cipher != "" {
		b.WriteString("data-ciphers " + p.Cipher + "\n")
	}
	if p.MaxClients != nil {
		fmt.Fprintf(&b, "max-clients %d\n", *p.MaxClients)
	}
	if p.DuplicateCN != nil && *p.DuplicateCN {
		b.WriteString("duplicate-cn\n")
	}

	for _, d := range p.DNS {
		b.WriteString("push \"dhcp-option DNS " + d + "\"\n")
	}
	if len(p.Routes) == 0 {
		// No routes means a full tunnel, which is what a VPN is usually for.
		b.WriteString("push \"redirect-gateway def1 bypass-dhcp\"\n")
	} else {
		for _, r := range p.Routes {
			b.WriteString("push \"route " + r + "\"\n")
		}
	}

	b.WriteString("keepalive 10 120\n")
	b.WriteString("persist-key\n")
	b.WriteString("persist-tun\n")
	b.WriteString("user nobody\n")
	b.WriteString("group nogroup\n")
	b.WriteString("verb 3\n")

	body := b.String()
	return markerLine(serviceID, checksumOf(body)) + body
}

func markerLine(serviceID int64, checksum string) string {
	return fmt.Sprintf("%s service_id=%d checksum=%s\n", markerPrefix, serviceID, checksum)
}

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

// bodyOf strips the marker line, leaving what the checksum covers.
func bodyOf(rendered string) string {
	if i := strings.IndexByte(rendered, '\n'); i >= 0 {
		return rendered[i+1:]
	}
	return ""
}

// serviceChecksum joins the three files' checksums.
//
// Joined rather than hashed together so Plan can see WHICH file changed: a
// config change restarts the service, a user change costs nothing, and
// collapsing them would charge every added user the price of a restart.
func serviceChecksum(conf, verify, users string) string {
	return conf + "." + verify + "." + users
}

func splitServiceChecksum(combined string) (conf, verify, users string, ok bool) {
	parts := strings.Split(combined, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
