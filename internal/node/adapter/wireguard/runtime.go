package wireguard

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ExecRuntime implements Runtime by shelling out to wg-quick and wg commands.
type ExecRuntime struct {
	CommandTimeout time.Duration
}

// NewExecRuntime returns a Runtime that uses system wg-quick and wg.
func NewExecRuntime() *ExecRuntime {
	return &ExecRuntime{CommandTimeout: 30 * time.Second}
}

func (r *ExecRuntime) timeout() time.Duration {
	if r.CommandTimeout <= 0 {
		return 30 * time.Second
	}
	return r.CommandTimeout
}

func (r *ExecRuntime) run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (r *ExecRuntime) Available(ctx context.Context) error {
	if _, err := exec.LookPath("wg-quick"); err != nil {
		return fmt.Errorf("%w: wg-quick not found in PATH: %w", ErrRuntimeUnavailable, err)
	}
	if _, err := exec.LookPath("wg"); err != nil {
		return fmt.Errorf("%w: wg not found in PATH: %w", ErrRuntimeUnavailable, err)
	}
	return nil
}

func (r *ExecRuntime) InterfaceUp(ctx context.Context, iface string) error {
	_, err := r.run(ctx, "wg-quick", "up", iface)
	return err
}

func (r *ExecRuntime) InterfaceDown(ctx context.Context, iface string) error {
	_, err := r.run(ctx, "wg-quick", "down", iface)
	return err
}

func (r *ExecRuntime) InterfaceStatus(ctx context.Context, iface string) (exists, up bool, err error) {
	// Check if interface exists in `wg show interfaces`
	out, err := r.run(ctx, "wg", "show", "interfaces")
	if err != nil {
		// wg show fails if no interfaces exist, which is fine
		return false, false, nil
	}

	interfaces := strings.Fields(out)
	for _, i := range interfaces {
		if i == iface {
			exists = true
			break
		}
	}

	if !exists {
		return false, false, nil
	}

	// Interface exists, check if it's actually up (has a listen port)
	out, err = r.run(ctx, "wg", "show", iface, "listen-port")
	if err != nil {
		return true, false, nil
	}

	port := strings.TrimSpace(out)
	if port == "(none)" || port == "" || port == "0" {
		return true, false, nil
	}

	return true, true, nil
}

func (r *ExecRuntime) ShowTransfer(ctx context.Context, iface string) (map[string]PeerTransfer, error) {
	out, err := r.run(ctx, "wg", "show", iface, "transfer")
	if err != nil {
		return nil, fmt.Errorf("wg show transfer: %w", err)
	}

	transfers := make(map[string]PeerTransfer)
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		publicKey := fields[0]
		rxBytes, err1 := strconv.ParseUint(fields[1], 10, 64)
		txBytes, err2 := strconv.ParseUint(fields[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}

		transfers[publicKey] = PeerTransfer{
			PublicKey: publicKey,
			RxBytes:   rxBytes,
			TxBytes:   txBytes,
		}
	}

	return transfers, scanner.Err()
}

func (r *ExecRuntime) SyncPeers(ctx context.Context, iface, configPath string) (bool, error) {
	// WireGuard supports hot peer updates via `wg syncconf`
	// This reads the config file and applies peer changes without restarting
	_, err := r.run(ctx, "wg", "syncconf", iface, configPath)
	if err != nil {
		// syncconf failed, caller should fall back to restart
		return false, err
	}
	return true, nil
}
