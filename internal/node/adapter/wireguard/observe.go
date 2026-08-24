package wireguard

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Observe reads the current state of WireGuard services on the host.
func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
	if err := a.rt.Available(ctx); err != nil {
		return adapter.Observed{}, err
	}

	// Scan /etc/wireguard for our managed configs
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No config directory means no services
			return adapter.Observed{Services: []adapter.ObservedService{}}, nil
		}
		return adapter.Observed{}, fmt.Errorf("read config dir: %w", err)
	}

	var services []adapter.ObservedService
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
			continue
		}

		path := a.configPath(0) // placeholder, will extract from file
		body, err := os.ReadFile(path)
		if err != nil {
			// File disappeared between ReadDir and ReadFile, skip it
			continue
		}

		// Parse marker to get service ID and checksum
		lines := strings.Split(string(body), "\n")
		if len(lines) == 0 {
			continue
		}
		serviceID, checksum, ok := parseMarker(lines[0])
		if !ok {
			// Config exists but has no marker - this is drift (manual creation)
			continue
		}

		// Extract body (everything after marker line)
		bodyContent := strings.Join(lines[1:], "\n")
		actualChecksum := checksumContent([]byte(bodyContent))

		// Check if config matches checksum
		checksumMatch := actualChecksum == checksum

		// Check if interface exists and is up
		iface := interfaceName(serviceID)
		_, _, err = a.rt.InterfaceStatus(ctx, iface)
		if err != nil {
			// Status check failed, mark as present but unmanaged (unknown state)
			services = append(services, adapter.ObservedService{
				ID:       serviceID,
				Present:  true,
				Managed:  false,
				Checksum: checksum,
			})
			continue
		}

		obs := adapter.ObservedService{
			ID:       serviceID,
			Present:  true,
			Managed:  checksumMatch,
			Checksum: checksum,
		}

		services = append(services, obs)
	}

	return adapter.Observed{Services: services}, nil
}
