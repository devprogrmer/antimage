package l2tp

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Observe reads the current state of L2TP/IPsec configuration on the host.
// It checks for managed files, parses markers, and reports checksums.
func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
	// L2TP adapter manages multiple files as one logical service.
	// If any managed file exists, the service is considered present.

	var obs adapter.Observed

	ipsecMgd, ipsecCS := a.checkFile(filepath.Join(a.confDir, "strongswan/ipsec.conf"))
	secretsMgd, secretsCS := a.checkFile(filepath.Join(a.confDir, "strongswan/ipsec.secrets"))
	xl2tpMgd, xl2tpCS := a.checkFile(filepath.Join(a.confDir, "xl2tpd/xl2tpd.conf"))
	chapMgd, chapCS := a.checkFile(filepath.Join(a.confDir, "ppp/chap-secrets"))
	optsMgd, optsCS := a.checkFile(filepath.Join(a.confDir, "ppp/options.xl2tpd"))

	// Service is present if any managed file exists.
	present := ipsecMgd || secretsMgd || xl2tpMgd || chapMgd || optsMgd

	// Service is fully managed only if all files are managed.
	managed := ipsecMgd && secretsMgd && xl2tpMgd && chapMgd && optsMgd

	if present {
		// Combine checksums to form a single service checksum.
		// This lets Plan detect changes in any file.
		combined := ipsecCS + ":" + secretsCS + ":" + xl2tpCS + ":" + chapCS + ":" + optsCS

		obs.Services = append(obs.Services, adapter.ObservedService{
			ID:       0, // Set during Plan when we know service ID
			Present:  true,
			Managed:  managed,
			Checksum: checksumOf(combined),
		})
	}

	return obs, nil
}

// checkFile reads a config file, checks for ownership marker, and extracts checksum.
func (a *Adapter) checkFile(path string) (managed bool, checksum string) {
	body, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}

	content := string(body)
	if !isManaged(content) {
		return false, ""
	}

	// Extract checksum from marker line.
	lines := strings.SplitN(content, "\n", 2)
	if len(lines) == 0 {
		return false, ""
	}

	_, checksum, ok := parseMarker(lines[0])
	if !ok {
		return false, ""
	}

	return true, checksum
}
