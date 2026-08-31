package hysteria2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Probe checks health of Hysteria2 services
func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	// Check if Hysteria2 binary is available
	if err := a.rt.Available(ctx); err != nil {
		return adapter.Health{
			OK:     false,
			Detail: err.Error(),
		}, nil
	}

	return adapter.Health{
		OK:     true,
		Detail: "hysteria2 runtime available",
	}, nil
}

// Restart bounces every currently configured Hysteria2 unit.
//
// Unlike xray and singbox, this adapter runs one systemd unit PER service
// rather than one process multiplexing every inbound, because Hysteria2 has
// no hot reload and no multi-listener mode of its own -- see ServerRestart's
// own comment. "Restart the adapter" therefore means restarting every unit
// Observe would currently enumerate, not a single process; scanning the same
// directory Observe reads keeps the two in agreement rather than defining
// the service set twice.
func (a *Adapter) Restart(ctx context.Context) error {
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: no hysteria2 services configured", adapter.ErrRestartUnsupported)
		}
		return fmt.Errorf("read config dir: %w", err)
	}

	var configPaths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, filePrefix) && strings.HasSuffix(name, fileSuffix) {
			configPaths = append(configPaths, filepath.Join(a.dir, name))
		}
	}
	if len(configPaths) == 0 {
		return fmt.Errorf("%w: no hysteria2 services configured", adapter.ErrRestartUnsupported)
	}

	var errs []error
	for _, configPath := range configPaths {
		if err := a.rt.ServerRestart(ctx, configPath); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filepath.Base(configPath), err))
		}
	}
	return errors.Join(errs...)
}
