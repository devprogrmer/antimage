package hysteria2

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Observe reads current state of Hysteria2 services on the host
func (a *Adapter) Observe(ctx context.Context) (adapter.Observed, error) {
	if err := a.rt.Available(ctx); err != nil {
		return adapter.Observed{}, err
	}

	// Scan config directory for managed configs
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if os.IsNotExist(err) {
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

		// Read config file
		configPath := filepath.Join(configDir, name)
		body, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}

		// Parse marker
		lines := strings.Split(string(body), "\n")
		if len(lines) == 0 {
			continue
		}
		serviceID, checksum, ok := parseMarker(lines[0])
		if !ok {
			// Not our file
			continue
		}

		// Recompute checksum
		bodyContent := strings.Join(lines[1:], "\n")
		actualChecksum := checksumContent([]byte(bodyContent))
		checksumMatch := actualChecksum == checksum

		// Check if server is running
		running, err := a.rt.ServerStatus(ctx, configPath)
		if err != nil {
			services = append(services, adapter.ObservedService{
				ID:       serviceID,
				Present:  true,
				Managed:  false,
				Checksum: checksum,
			})
			continue
		}

		services = append(services, adapter.ObservedService{
			ID:       serviceID,
			Present:  true,
			Managed:  checksumMatch,
			Checksum: checksum,
		})
		_ = running // Will be used for status tracking
	}

	return adapter.Observed{Services: services}, nil
}
