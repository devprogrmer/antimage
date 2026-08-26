package agent

import (
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// C2's attribution crosses four boundaries between the Xray counter and the
// stored row. This is the agent's: what the adapters computed has to reach the
// wire unchanged.
//
// Worth its own test because a break here is invisible from either side. The
// adapter would still attribute correctly, the panel would still store exactly
// what it was handed, and every attribution on the platform would quietly be
// NULL.
func TestProtoSamplesCarryTheService(t *testing.T) {
	in := []adapter.UsageSample{
		{SubjectID: 1, ServiceID: 10, UplinkBytes: 100, DownlinkBytes: 200},
		// The same subject on a second inbound: two samples, not one merged
		// total, and the pair is only distinguishable by ServiceID.
		{SubjectID: 1, ServiceID: 20, UplinkBytes: 5, DownlinkBytes: 7},
		// An adapter that cannot attribute. Zero must survive as zero rather
		// than being filled in with a neighbour's value.
		{SubjectID: 2, ServiceID: 0, UplinkBytes: 1, DownlinkBytes: 2},
	}

	got := protoSamplesFrom(in)
	if len(got) != len(in) {
		t.Fatalf("converted %d samples, want %d", len(got), len(in))
	}
	for i, want := range in {
		g := got[i]
		if g.SubjectId != want.SubjectID {
			t.Errorf("sample %d subject = %d, want %d", i, g.SubjectId, want.SubjectID)
		}
		if g.ServiceId != want.ServiceID {
			t.Errorf("sample %d service = %d, want %d; the attribution was lost "+
				"between the adapter and the wire", i, g.ServiceId, want.ServiceID)
		}
		if g.UplinkBytes != want.UplinkBytes || g.DownlinkBytes != want.DownlinkBytes {
			t.Errorf("sample %d bytes = %d/%d, want %d/%d", i,
				g.UplinkBytes, g.DownlinkBytes, want.UplinkBytes, want.DownlinkBytes)
		}
	}
}
