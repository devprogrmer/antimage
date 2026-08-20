package l2tp

import (
	"fmt"
	"os/exec"
)

// Service control functions wrap systemctl for strongSwan and xl2tpd.

func startService(name string) error {
	cmd := exec.Command("systemctl", "start", name)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl start %s: %w", name, err)
	}
	return nil
}

func stopService(name string) error {
	cmd := exec.Command("systemctl", "stop", name)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl stop %s: %w", name, err)
	}
	return nil
}

func restartService(name string) error {
	cmd := exec.Command("systemctl", "restart", name)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl restart %s: %w", name, err)
	}
	return nil
}

func reloadService(name string) error {
	cmd := exec.Command("systemctl", "reload", name)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl reload %s: %w", name, err)
	}
	return nil
}

func isServiceActive(name string) bool {
	cmd := exec.Command("systemctl", "is-active", "--quiet", name)
	return cmd.Run() == nil
}

// reloadStrongSwanCreds tells strongSwan to reload credentials without
// dropping tunnels. This is what makes HotUserAdd=true possible.
func reloadStrongSwanCreds() error {
	cmd := exec.Command("swanctl", "--load-creds")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("swanctl --load-creds: %w", err)
	}
	return nil
}
