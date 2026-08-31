package l2tp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Restart bounces both services this adapter manages: strongSwan carries the
// IPsec layer, xl2tpd carries the L2TP layer, and a client tunnel needs both
// up. xl2tpd.conf existing is the same marker Apply's own install step uses
// to know the service has been configured at all.
func (a *Adapter) Restart(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(a.confDir, "xl2tpd", "xl2tpd.conf")); err != nil {
		return fmt.Errorf("%w: no L2TP/IPsec service configured", adapter.ErrRestartUnsupported)
	}
	var errs []error
	if err := restartService("strongswan"); err != nil {
		errs = append(errs, err)
	}
	if err := restartService("xl2tpd"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
