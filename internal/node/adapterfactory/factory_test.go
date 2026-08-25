package adapterfactory

import (
	"strings"
	"testing"

	"github.com/amyrm/antimage/internal/node/agent"
)

func cfg(t *testing.T, ads ...agent.AdapterConfig) *agent.Config {
	t.Helper()
	return &agent.Config{StateDir: t.TempDir(), Adapters: ads}
}

func kindsOf(t *testing.T, r *agent.Registry) []string {
	t.Helper()
	var out []string
	for _, d := range r.Descriptors() {
		out = append(out, string(d.Kind))
	}
	return out
}

// Every kind the factory advertises must actually build. A supported list that
// names something Build refuses would send an operator to a config that cannot
// start.
func TestEverySupportedKindBuilds(t *testing.T) {
	for _, kind := range SupportedKinds() {
		r, err := Build(cfg(t, agent.AdapterConfig{Kind: kind}))
		if err != nil {
			t.Errorf("%s is advertised as supported but does not build: %v", kind, err)
			continue
		}
		if got := kindsOf(t, r); len(got) != 1 || got[0] != kind {
			t.Errorf("%s built as %v", kind, got)
		}
	}
}

// The point of the whole exercise: several protocols on one node.
func TestSeveralAdaptersOnOneNode(t *testing.T) {
	r, err := Build(cfg(t,
		agent.AdapterConfig{Kind: "xray"},
		agent.AdapterConfig{Kind: "wireguard"},
		agent.AdapterConfig{Kind: "hysteria2"},
	))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if r.Len() != 3 {
		t.Fatalf("built %d adapters, want 3", r.Len())
	}
	// Declaration order is preserved: it fixes step order within an apply run.
	want := []string{"xray", "wireguard", "hysteria2"}
	got := kindsOf(t, r)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("adapters = %v, want %v (declaration order)", got, want)
		}
	}
}

// An unknown kind must refuse to start, not be skipped.
//
// An operator who typed "wiregaurd" otherwise gets a node that starts happily
// and serves nothing, and nothing in the logs connects the two.
func TestUnknownKindIsRefused(t *testing.T) {
	_, err := Build(cfg(t, agent.AdapterConfig{Kind: "wiregaurd"}))
	if err == nil {
		t.Fatal("an unknown adapter kind was accepted; the node would start and " +
			"serve nothing with no indication why")
	}
	if !strings.Contains(err.Error(), "wiregaurd") {
		t.Errorf("error does not quote what was typed: %v", err)
	}
	// The message has to say what IS supported, or the operator is left guessing.
	for _, kind := range []string{"xray", "wireguard"} {
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("error does not list %s among supported kinds: %v", kind, err)
		}
	}
}

// Two adapters of one kind would manage the same files and overwrite each
// other on every pass, so the registry refuses them and Build surfaces that.
func TestDuplicateKindsAreRefused(t *testing.T) {
	_, err := Build(cfg(t,
		agent.AdapterConfig{Kind: "wireguard"},
		agent.AdapterConfig{Kind: "wireguard", ConfigDir: "/etc/wireguard-2"},
	))
	if err == nil {
		t.Fatal("two adapters of the same kind were accepted")
	}
}

// A config with no adapters keeps working across the upgrade rather than
// refusing to start -- but it is the stub, which serves nothing, so the caller
// is told to say so.
func TestNoAdaptersFallsBackToTheStub(t *testing.T) {
	c := cfg(t)
	if !NoAdaptersConfigured(c) {
		t.Error("NoAdaptersConfigured = false for a config declaring none")
	}
	r, err := Build(c)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := kindsOf(t, r); len(got) != 1 || got[0] != "stub" {
		t.Errorf("adapters = %v, want [stub]", got)
	}
}

func TestNoAdaptersConfiguredIsFalseWhenSomeAre(t *testing.T) {
	if NoAdaptersConfigured(cfg(t, agent.AdapterConfig{Kind: "xray"})) {
		t.Error("NoAdaptersConfigured = true although one was declared")
	}
}

// Whether Xray can add a user without restarting depends on THIS host having
// configured the management API, which is why the capability is reported per
// node rather than per adapter type.
func TestXrayHotAddFollowsTheConfiguredAPIAddress(t *testing.T) {
	withAPI, err := Build(cfg(t, agent.AdapterConfig{
		Kind: "xray", APIAddress: "127.0.0.1:10085",
	}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !withAPI.Descriptors()[0].Caps.HotUserAdd {
		t.Error("HotUserAdd = false although the management API is configured; " +
			"every user change would cost a restart it does not need")
	}

	without, err := Build(cfg(t, agent.AdapterConfig{Kind: "xray"}))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if without.Descriptors()[0].Caps.HotUserAdd {
		t.Error("HotUserAdd = true with no management API configured; the panel " +
			"would plan hot adds that cannot work")
	}
}

// Adapters keep their own state directory, so two of them cannot collide over
// one sidecar file.
func TestAdaptersGetSeparateStateDirectories(t *testing.T) {
	c := cfg(t,
		agent.AdapterConfig{Kind: "wireguard"},
		agent.AdapterConfig{Kind: "hysteria2"},
	)
	if _, err := Build(c); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// The paths are internal to each adapter, so this asserts the rule the
	// factory applies rather than reaching inside: distinct kinds produce
	// distinct directories under the node's state dir.
	wg := stateDirFor(c, "wireguard")
	h2 := stateDirFor(c, "hysteria2")
	if wg == h2 {
		t.Fatalf("both adapters would keep state in %s", wg)
	}
	if !strings.HasPrefix(wg, c.StateDir) {
		t.Errorf("state dir %s is outside the node's state dir %s", wg, c.StateDir)
	}
}

// Every descriptor carries the schema the panel needs to validate params and
// an editor needs to build a form. A kind that published none could be
// declared here and then never be usable.
func TestEveryBuiltAdapterPublishesAServiceSchema(t *testing.T) {
	for _, kind := range SupportedKinds() {
		r, err := Build(cfg(t, agent.AdapterConfig{Kind: kind}))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if len(r.Descriptors()[0].Caps.ServiceSchema) == 0 {
			t.Errorf("%s publishes no ServiceSchema, so the panel cannot validate "+
				"its params and an editor cannot offer it", kind)
		}
	}
}
