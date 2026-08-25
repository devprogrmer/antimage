package wireguard

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// InterfaceUp brings the interface up from configPath.
//
// wg-quick is given the PATH rather than the interface name: a bare name is
// resolved against /etc/wireguard, which is not necessarily where the adapter
// wrote the file. wg-quick derives the interface name from the basename, which
// is why the config filename and the interface name have to agree.
func (r *ExecRuntime) InterfaceUp(ctx context.Context, _, configPath string) error {
	_, err := r.run(ctx, "wg-quick", "up", configPath)
	return err
}

func (r *ExecRuntime) InterfaceDown(ctx context.Context, _, configPath string) error {
	_, err := r.run(ctx, "wg-quick", "down", configPath)
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

// SyncPeers applies a peer change to a running interface, without restarting.
//
// The config is passed through `wg-quick strip` first, and that is not optional.
// `wg syncconf` speaks the BARE wg format; the file antimage writes is a
// wg-quick config, which additionally carries Address, DNS and MTU in
// [Interface]. wg does not know those keys and refuses the whole file:
//
//	Line unrecognized: `Address=10.99.0.1/24'
//	Configuration parsing error
//
// So every hot peer add failed with a parse error, and fell back to a restart
// that disconnected everyone. Nothing caught it because nothing had ever
// executed this method -- Apply returned "not yet implemented", which is the
// whole of AD-3. The real-runtime job found it on its first pass.
//
// `wg-quick strip` is wg-quick's own filter for exactly this, so the two views
// of the config cannot drift: one file on disk, stripped by the tool that
// wrote its format.
func (r *ExecRuntime) SyncPeers(ctx context.Context, iface, configPath string) (bool, error) {
	stripped, err := r.run(ctx, "wg-quick", "strip", configPath)
	if err != nil {
		return false, fmt.Errorf("strip %s for syncconf: %w", configPath, err)
	}

	// syncconf takes a path, not stdin, so the stripped form has to land in a
	// file. It carries the interface private key, so it is created 0600 in the
	// same directory as the config -- not in a shared temp dir -- and removed
	// as soon as wg has read it.
	tmp, err := os.CreateTemp(filepath.Dir(configPath), ".antimage-sync-*.conf")
	if err != nil {
		return false, fmt.Errorf("create stripped config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("chmod stripped config: %w", err)
	}
	if _, err := tmp.WriteString(stripped); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write stripped config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close stripped config: %w", err)
	}

	if _, err := r.run(ctx, "wg", "syncconf", iface, tmpName); err != nil {
		// The caller turns this into a failed step; the reconciler plans a
		// restart on the next pass, which is disruptive but correct.
		return false, err
	}
	return true, nil
}
