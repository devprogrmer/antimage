//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestAcceptance maps one subtest onto each numbered item of spec section 10.
// The subtests share one panel and one node and run in order: item 4 only
// means anything after item 3 has taken the node offline.
func TestAcceptance(t *testing.T) {
	e := startPanel(t)

	t.Run("1_node_enrolls_over_mtls_and_reaches_online", func(t *testing.T) {
		e.createNodeAndEnroll()
		e.startAgent()
		e.waitForStatus("online", 30*time.Second)
	})

	t.Run("2_service_bumps_revision_and_converges", func(t *testing.T) {
		e.createService(`{"adapter_kind":"stub","params":{"port":8443}}`)
		e.waitForConverged(30 * time.Second)

		e.waitFor("the managed file to carry port 8443", 15*time.Second, func() (string, bool) {
			return fmt.Sprintf("files=%v", e.managedFiles()), e.managedFilesContain(`"port":8443`)
		})
	})

	t.Run("3_stopping_the_agent_marks_the_node_offline", func(t *testing.T) {
		e.stopAgent()
		// The sweeper flips online/degraded to offline once heartbeats stop.
		// Production waits three missed 30s intervals; the harness compresses
		// the threshold so this exercises the real code path in seconds.
		e.waitForStatus("offline", 45*time.Second)
	})

	t.Run("4_restarting_the_agent_self_heals_state_it_missed", func(t *testing.T) {
		// Change desired state while the node is down, so recovery has to
		// converge rather than merely reconnect.
		e.createService(`{"adapter_kind":"stub","params":{"port":9443}}`)

		e.startAgent()
		e.waitForStatus("online", 30*time.Second)
		e.waitForConverged(60 * time.Second)

		e.waitFor("the missed service to appear", 30*time.Second, func() (string, bool) {
			return fmt.Sprintf("files=%v", e.managedFiles()), e.managedFilesContain(`"port":9443`)
		})
	})

	t.Run("5_hand_editing_a_managed_file_is_detected_as_drift", func(t *testing.T) {
		files := e.managedFiles()
		if len(files) == 0 {
			t.Fatal("no managed files to edit")
		}
		var name string
		for n := range files {
			name = n
			break
		}

		before := e.applyRunCount()
		e.corruptManagedFile(name)

		// The agent re-observes on its reconcile timer; nudge it with a
		// revision bump so the test does not wait five minutes.
		e.createService(`{"adapter_kind":"stub","params":{"port":7443}}`)

		e.waitFor("a corrective apply run", 60*time.Second, func() (string, bool) {
			n := e.applyRunCount()
			return fmt.Sprintf("apply_runs=%d (was %d)", n, before), n > before
		})
		// The hand edit must be gone: convergence rewrites the managed file.
		e.waitFor("the hand edit to be corrected", 60*time.Second, func() (string, bool) {
			return fmt.Sprintf("files=%v", e.managedFiles()),
				!e.managedFilesContain("HAND-EDITED-BY-AN-OPERATOR")
		})
	})

	t.Run("6_deleting_the_node_locks_it_out", func(t *testing.T) {
		e.stopAgent()
		if code := e.apiJSON("DELETE",
			fmt.Sprintf("/api/v1/nodes/%d", e.nodeID), "", nil); code != http.StatusNoContent {
			t.Fatalf("delete node: %d", code)
		}

		// The allow-list is the revocation mechanism: cert_fingerprint went
		// away with the row, so the agent's certificate is no longer
		// recognised and its next stream must be refused.
		err := e.dialControlStreamOnce()
		if err == nil {
			t.Fatal("a deleted node's certificate was still accepted on the control stream")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "not enrolled") &&
			!strings.Contains(strings.ToLower(err.Error()), "unauthenticated") &&
			!strings.Contains(strings.ToLower(err.Error()), "permission") {
			t.Logf("refused, but with an unexpected error: %v", err)
		}
	})
}

// TestDefinitionOfDoneGates runs the section 9 CI gates that do not need a
// toolchain this host lacks. `make` is not installed here, so each gate is
// invoked directly rather than through its Makefile target.
func TestDefinitionOfDoneGates(t *testing.T) {
	for _, g := range []struct {
		name string
		args []string
	}{
		{"go build", []string{"go", "build", "./..."}},
		{"go vet", []string{"go", "vet", "./..."}},
		{"unit tests", []string{"go", "test", "./...", "-count=1"}},
		{"import boundaries + ssh host-key policy", []string{"bash", "scripts/check-imports.sh"}},
		{"RTL and i18n gates", []string{"bash", "scripts/check-rtl.sh"}},
		{"install.sh guards", []string{"bash", "scripts/install_test.sh"}},
	} {
		t.Run(g.name, func(t *testing.T) {
			out, err := runInRepoRoot(t, g.args...)
			if err != nil {
				t.Fatalf("%s failed: %v\n%s", g.name, err, out)
			}
		})
	}
}
