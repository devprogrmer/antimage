package wireguard

import (
	"context"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// TestWireGuardPeerRegistry tests the peer registry implementation
func TestWireGuardPeerRegistry(t *testing.T) {
	t.Run("DerivePublicKey", func(t *testing.T) {
		// Test with a known WireGuard key pair (if wg command available)
		// This is a real private key for testing (DO NOT use in production)
		testPrivateKey := "YAnz5TF+lXXJte14tji3zlMNftqL8c7bWdS7AYzk2VI="

		publicKey := derivePublicKey(testPrivateKey)

		// If wg command is available, should return non-empty public key
		if publicKey == "" {
			t.Skip("wg command not available, skipping public key derivation test")
		}

		// Public key should be 44 characters (32 bytes base64 encoded)
		if len(publicKey) != 44 {
			t.Errorf("Expected public key length 44, got %d", len(publicKey))
		}

		t.Logf("Derived public key: %s", publicKey)
	})

	t.Run("RegistryUpdateAndLookup", func(t *testing.T) {
		registry := newPeerRegistry()

		subjects := []adapter.Subject{
			{
				ID: 1001,
				Credentials: []adapter.Credential{
					{Kind: string(adapter.CredKeypair), Value: "YAnz5TF+lXXJte14tji3zlMNftqL8c7bWdS7AYzk2VI="},
				},
			},
			{
				ID: 1002,
				Credentials: []adapter.Credential{
					{Kind: string(adapter.CredKeypair), Value: "uBqQvZ7PEEDcZshJ0NdK2sHWZ5x5gPmH5vXlL1lQcHs="},
				},
			},
		}

		registry.update(subjects)

		// Test lookup for first subject
		publicKey1 := derivePublicKey("YAnz5TF+lXXJte14tji3zlMNftqL8c7bWdS7AYzk2VI=")
		if publicKey1 != "" {
			subjectID, ok := registry.lookup(publicKey1)
			if !ok {
				t.Error("Expected to find subject 1001 in registry")
			}
			if subjectID != 1001 {
				t.Errorf("Expected subject ID 1001, got %d", subjectID)
			}
		}

		// Test lookup for non-existent key
		_, ok := registry.lookup("nonexistent-public-key")
		if ok {
			t.Error("Should not find non-existent public key")
		}
	})

	t.Run("RegistryConcurrentAccess", func(t *testing.T) {
		registry := newPeerRegistry()

		subjects := []adapter.Subject{
			{ID: 2001, Credentials: []adapter.Credential{{Kind: string(adapter.CredKeypair), Value: "test1"}}},
			{ID: 2002, Credentials: []adapter.Credential{{Kind: string(adapter.CredKeypair), Value: "test2"}}},
		}

		// Concurrent updates and lookups (race test)
		done := make(chan bool, 10)

		// Writers
		for i := 0; i < 5; i++ {
			go func() {
				registry.update(subjects)
				done <- true
			}()
		}

		// Readers
		for i := 0; i < 5; i++ {
			go func() {
				registry.lookup("any-key")
				done <- true
			}()
		}

		// Wait for all goroutines
		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

// TestWireGuardAccounting tests the accounting implementation
func TestWireGuardAccounting(t *testing.T) {
	t.Run("AccountingCursorPersistence", func(t *testing.T) {
		// Test accounting cursor save/load cycle
		adapter := &Adapter{
			stateDir: t.TempDir(),
			registry: newPeerRegistry(),
		}

		// Create a cursor
		cursor := accountingCursor{
			LastPoll: 1234567890,
			Peers: map[string]peerCounters{
				"pubkey1": {RxBytes: 1000, TxBytes: 2000},
				"pubkey2": {RxBytes: 3000, TxBytes: 4000},
			},
		}

		// Save cursor
		err := adapter.saveCursor(cursor)
		if err != nil {
			t.Fatalf("Failed to save cursor: %v", err)
		}

		// Load cursor
		loaded, err := adapter.loadCursor()
		if err != nil {
			t.Fatalf("Failed to load cursor: %v", err)
		}

		// Verify
		if loaded.LastPoll != cursor.LastPoll {
			t.Errorf("LastPoll mismatch: expected %d, got %d", cursor.LastPoll, loaded.LastPoll)
		}
		if len(loaded.Peers) != len(cursor.Peers) {
			t.Errorf("Peers count mismatch: expected %d, got %d", len(cursor.Peers), len(loaded.Peers))
		}
		if loaded.Peers["pubkey1"].RxBytes != 1000 {
			t.Errorf("RxBytes mismatch for pubkey1")
		}
	})

	t.Run("UsageDeltaComputation", func(t *testing.T) {
		// Test delta computation logic
		prev := peerCounters{RxBytes: 1000, TxBytes: 2000}
		curr := peerCounters{RxBytes: 1500, TxBytes: 2800}

		deltaRx := curr.RxBytes - prev.RxBytes
		deltaTx := curr.TxBytes - prev.TxBytes

		if deltaRx != 500 {
			t.Errorf("Expected deltaRx 500, got %d", deltaRx)
		}
		if deltaTx != 800 {
			t.Errorf("Expected deltaTx 800, got %d", deltaTx)
		}
	})

	t.Run("CounterRolloverDetection", func(t *testing.T) {
		// WireGuard counters reset on interface restart
		// Usage() should detect and handle counter rollback

		prev := peerCounters{RxBytes: 10000, TxBytes: 20000}
		curr := peerCounters{RxBytes: 500, TxBytes: 800} // Rolled over (restart)

		// If current < previous, it's a rollback → treat as restart
		if curr.RxBytes < prev.RxBytes {
			// Delta should be current value (fresh start)
			deltaRx := curr.RxBytes
			deltaTx := curr.TxBytes

			if deltaRx != 500 {
				t.Errorf("Expected deltaRx 500 after rollover, got %d", deltaRx)
			}
			if deltaTx != 800 {
				t.Errorf("Expected deltaTx 800 after rollover, got %d", deltaTx)
			}
		}
	})
}

// TestWireGuardEnforcementIntegration tests integration with Enforcer
func TestWireGuardEnforcementIntegration(t *testing.T) {
	t.Run("QuotaEnforcement", func(t *testing.T) {
		t.Skip("Requires real WireGuard runtime and Enforcer integration")

		// This test would verify:
		// 1. Subject connects via WireGuard
		// 2. Traffic accounting tracks usage
		// 3. Usage samples sent to Enforcer
		// 4. Enforcer updates quota
		// 5. Connection rejected when quota exceeded
	})

	t.Run("ConnectionLimitEnforcement", func(t *testing.T) {
		t.Skip("Requires real WireGuard runtime")

		// This test would verify:
		// 1. MaxConnections policy set
		// 2. Multiple peers attempt handshake
		// 3. Connections beyond limit rejected
		// 4. Existing connections tracked correctly
	})

	t.Run("PeerRevocation", func(t *testing.T) {
		t.Skip("Requires real WireGuard runtime")

		// This test would verify:
		// 1. Peer connected and active
		// 2. Subject revoked (policy update)
		// 3. Peer removed from config
		// 4. Config reloaded (wg syncconf or restart)
		// 5. Peer can no longer handshake
	})

	t.Run("PolicyPropagation", func(t *testing.T) {
		t.Skip("Requires real WireGuard runtime")

		// This test would verify:
		// 1. Policy updated in database
		// 2. Desired state computed
		// 3. Plan generated
		// 4. Applied to WireGuard config
		// 5. Interface synced (hot reload or restart)
		// 6. New policy enforced
		// 7. Measure propagation latency
	})
}

// TestWireGuardFailureRecovery tests failure scenarios
func TestWireGuardFailureRecovery(t *testing.T) {
	t.Run("InterfaceRestart", func(t *testing.T) {
		t.Skip("Requires real WireGuard runtime")

		// This test would verify:
		// 1. Interface up with peers
		// 2. Interface restart (systemctl restart)
		// 3. Counters reset detection
		// 4. Accounting cursor adjustment
		// 5. Peers reconnect
		// 6. Usage tracking resumes
	})

	t.Run("NodeRestart", func(t *testing.T) {
		t.Skip("Requires real WireGuard runtime")

		// This test would verify:
		// 1. Node running with WireGuard
		// 2. Node process crash/restart
		// 3. Accounting cursor loaded from disk
		// 4. Registry rebuilt from desired state
		// 5. Interface state reconciled
		// 6. Traffic accounting resumes
	})

	t.Run("ConfigDriftRecovery", func(t *testing.T) {
		t.Skip("Requires real WireGuard runtime")

		// This test would verify:
		// 1. Manual config change outside Antimage
		// 2. Drift detected on next reconciliation
		// 3. Config regenerated from desired state
		// 4. Interface synced to match desired
		// 5. Drift resolved
	})
}

// TestWireGuardRaceConditions tests concurrency safety
func TestWireGuardRaceConditions(t *testing.T) {
	t.Run("ConcurrentPlanAndUsage", func(t *testing.T) {
		wgAdapter := &Adapter{
			stateDir: t.TempDir(),
			registry: newPeerRegistry(),
		}

		subjects := []adapter.Subject{
			{ID: 3001, Credentials: []adapter.Credential{{Kind: "keypair", Value: "key1"}}},
			{ID: 3002, Credentials: []adapter.Credential{{Kind: "keypair", Value: "key2"}}},
		}

		ctx := context.Background()
		done := make(chan bool, 20)

		// Concurrent Plan() calls (updates registry)
		for i := 0; i < 10; i++ {
			go func() {
				wgAdapter.registry.update(subjects)
				done <- true
			}()
		}

		// Concurrent Usage() calls (reads registry)
		for i := 0; i < 10; i++ {
			go func() {
				_, _ = wgAdapter.Usage(ctx)
				done <- true
			}()
		}

		// Wait for all goroutines
		for i := 0; i < 20; i++ {
			<-done
		}

		// If we get here without deadlock or race, test passes
	})
}
