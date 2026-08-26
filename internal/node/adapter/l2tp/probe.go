package l2tp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// Probe checks L2TP/IPsec service health.
func (a *Adapter) Probe(ctx context.Context) (adapter.Health, error) {
	// Check if strongSwan is running.
	if !isServiceActive("strongswan") {
		return adapter.Health{
			OK:     false,
			Detail: "strongswan service not running",
		}, nil
	}

	// Check if xl2tpd is running.
	if !isServiceActive("xl2tpd") {
		return adapter.Health{
			OK:     false,
			Detail: "xl2tpd service not running",
		}, nil
	}

	// Check listening ports for strongSwan and xl2tpd.
	// UDP 500: IKE (Internet Key Exchange)
	// UDP 4500: NAT-T (NAT Traversal)
	// UDP 1701: L2TP
	requiredPorts := []int{500, 4500, 1701}
	for _, port := range requiredPorts {
		if !isPortListening(port) {
			return adapter.Health{
				OK:     false,
				Detail: fmt.Sprintf("UDP port %d not listening", port),
			}, nil
		}
	}

	return adapter.Health{
		OK:     true,
		Detail: "strongswan and xl2tpd running, ports 500/4500/1701 listening",
	}, nil
}

// isPortListening checks if a UDP port is listening.
// It attempts to bind to the port; if the bind fails with "address already in use",
// the port is listening. Any other error or successful bind means it's not in use.
func isPortListening(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		// If we get an error binding, check if it's "address already in use".
		// On Windows, the error message may be different from Unix.
		errStr := err.Error()
		if containsAny(errStr, []string{"address already in use", "bind", "Only one usage"}) {
			return true
		}
		// Other errors mean we couldn't verify the port.
		return false
	}
	// If we successfully bound to the port, it's NOT in use.
	// Close it and return false.
	_ = conn.Close() // Ignore close error - we just checked if port is free
	return false
}

// containsAny checks if a string contains any of the substrings.
func containsAny(s string, substrs []string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// isPortReachable is an alternative approach that attempts to dial the port.
// This is less reliable for UDP but can be used as a fallback.
func isPortReachable(port int) bool {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("udp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close() // Ignore close error - we're only checking reachability
	return true
}
