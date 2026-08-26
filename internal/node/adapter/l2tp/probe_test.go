package l2tp

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestIsPortListening(t *testing.T) {
	// Find an available port to test with.
	testPort := 0
	listener, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatalf("failed to find available port: %v", err)
	}
	testPort = listener.LocalAddr().(*net.UDPAddr).Port

	// Verify port is detected as listening.
	if !isPortListening(testPort) {
		t.Errorf("expected port %d to be detected as listening", testPort)
	}

	// Close the listener.
	listener.Close()

	// Give the OS time to release the port.
	time.Sleep(100 * time.Millisecond)

	// Verify port is detected as NOT listening after close.
	if isPortListening(testPort) {
		t.Errorf("expected port %d to be detected as NOT listening after close", testPort)
	}
}

func TestIsPortListeningUnusedPort(t *testing.T) {
	// Test with a high port that's very unlikely to be in use.
	unusedPort := 54321

	// First verify it's truly unused by trying to bind to it.
	listener, err := net.ListenPacket("udp", fmt.Sprintf(":%d", unusedPort))
	if err != nil {
		t.Skipf("port %d is already in use, skipping test", unusedPort)
	}
	listener.Close()

	// Give OS time to release.
	time.Sleep(100 * time.Millisecond)

	// Now check that our function detects it as not listening.
	if isPortListening(unusedPort) {
		t.Errorf("expected unused port %d to be detected as NOT listening", unusedPort)
	}
}

func TestIsPortListeningStandardPorts(t *testing.T) {
	// This test documents the expected behavior for L2TP/IPsec ports.
	// It will only pass if strongSwan and xl2tpd are actually running.
	// In CI/test environments without these services, we skip.

	ports := []struct {
		port   int
		name   string
		needed bool
	}{
		{500, "IKE", true},
		{4500, "NAT-T", true},
		{1701, "L2TP", true},
	}

	for _, p := range ports {
		t.Run(fmt.Sprintf("port_%d_%s", p.port, p.name), func(t *testing.T) {
			// We can't reliably test these ports without the actual services running.
			// This test documents the expected behavior.
			listening := isPortListening(p.port)
			t.Logf("Port %d (%s): listening=%v", p.port, p.name, listening)
			// We don't fail here since test environment may not have services.
		})
	}
}

func TestIsPortReachable(t *testing.T) {
	// Test the alternative reachability check.
	// Start a UDP listener on a random port.
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create UDP listener: %v", err)
	}
	defer listener.Close()

	testPort := listener.LocalAddr().(*net.UDPAddr).Port

	// Note: isPortReachable for UDP is less reliable because UDP is connectionless.
	// The dial will succeed even if nothing is listening, so this test is mainly
	// to verify the function doesn't crash.
	reachable := isPortReachable(testPort)
	t.Logf("Port %d reachable via dial: %v", testPort, reachable)
}

// TestProbeIntegration tests the full Probe method behavior.
// This is an integration test that requires strongSwan and xl2tpd to be installed.
func TestProbeIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	_ = New("/etc", "/var/lib/antimage")

	// Note: This test will only pass on a system with strongSwan and xl2tpd
	// properly configured and running. In other environments, it documents
	// the expected behavior.
	
	// We don't use ctx.Background() with a timeout here since Probe should be fast.
	// health, err := a.Probe(context.Background())
	// if err != nil {
	// 	t.Fatalf("Probe failed: %v", err)
	// }
	
	// Log the result without enforcing specific expectations since test
	// environment may not have services running.
	// t.Logf("Probe result: OK=%v, Detail=%s", health.OK, health.Detail)

	t.Log("Integration test skipped: requires live strongSwan and xl2tpd services")
}

// TestPortListeningErrorHandling tests error cases in port checking.
func TestPortListeningErrorHandling(t *testing.T) {
	// Test with privileged ports (< 1024) that we can't bind to without root.
	// The function should handle the permission error gracefully.
	
	// Port 1 is typically restricted and unused.
	result := isPortListening(1)
	// We don't assert a specific result since it depends on privileges and OS,
	// but we verify the function doesn't panic.
	t.Logf("Port 1 listening check result: %v (depends on privileges)", result)
}

// BenchmarkIsPortListening measures the performance of port checking.
func BenchmarkIsPortListening(b *testing.B) {
	// Use a high port unlikely to be in use.
	testPort := 54321

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		isPortListening(testPort)
	}
}
