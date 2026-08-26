package xray

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amyrm/antimage/internal/node/adapter"
)

// C2 at the edge: Xray keeps ONE traffic counter per user tag, so the tag is
// the only place attribution can be created. These cover the two halves --
// the adapter writes a tag that distinguishes services, and it reads that
// distinction back out of the stats.

// The defect C2 had to fix before anything else could work.
//
// A subject entitled to two inbounds used to get the SAME email on both, so
// Xray summed their traffic into a single counter and no later stage could tell
// the two inbounds apart. No amount of plumbing downstream recovers that: the
// information is destroyed at the edge, before it is ever reported.
func TestTheSameSubjectGetsADifferentTagPerService(t *testing.T) {
	d := adapter.Desired{
		Subjects: []adapter.Subject{{
			ID:          1,
			Credentials: []adapter.Credential{{Kind: "uuid", Value: "u-1"}},
		}},
	}

	onA, err := usersFor(d, 10)
	if err != nil {
		t.Fatalf("usersFor(10): %v", err)
	}
	onB, err := usersFor(d, 20)
	if err != nil {
		t.Fatalf("usersFor(20): %v", err)
	}
	if len(onA) != 1 || len(onB) != 1 {
		t.Fatalf("got %d and %d users, want 1 each", len(onA), len(onB))
	}
	if onA[0].Email == onB[0].Email {
		t.Fatalf("subject 1 has the tag %q on BOTH services; Xray keeps one "+
			"counter per tag, so the two inbounds' traffic is summed into it "+
			"and C2 has nothing left to attribute", onA[0].Email)
	}
	if onA[0].ServiceID != 10 || onB[0].ServiceID != 20 {
		t.Errorf("service ids = %d and %d, want 10 and 20",
			onA[0].ServiceID, onB[0].ServiceID)
	}
	// The credential must be the same one: it is the same person on two
	// inbounds, and a different credential per inbound would mean two identities.
	if onA[0].Credential != onB[0].Credential {
		t.Errorf("the same subject got different credentials per service (%q vs %q)",
			onA[0].Credential, onB[0].Credential)
	}
}

// Plan renders each inbound with its own tags, so the config Xray actually
// loads is what creates the separate counters.
func TestPlanRendersPerServiceTags(t *testing.T) {
	a, _, _ := newAdapter(t, true)
	d := adapter.Desired{
		Subjects: []adapter.Subject{{
			ID:          7,
			Credentials: []adapter.Credential{{Kind: "uuid", Value: "u-7"}},
		}},
		Services: []adapter.Service{
			{ID: 10, Kind: string(Kind), Enabled: true,
				Params: json.RawMessage(`{"protocol":"vless","port":443}`)},
			{ID: 20, Kind: string(Kind), Enabled: true,
				Params: json.RawMessage(`{"protocol":"vless","port":8443}`)},
		},
	}

	plan, err := a.Plan(context.Background(), d, adapter.Observed{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	seen := map[int64]string{}
	for _, step := range plan.Steps {
		if step.Kind != StepWriteService {
			continue
		}
		var p stepPayload
		if err := json.Unmarshal(step.Payload, &p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if len(p.Users) != 1 {
			t.Fatalf("service %d carries %d users, want 1", step.ServiceID, len(p.Users))
		}
		seen[step.ServiceID] = p.Users[0]
	}

	if len(seen) != 2 {
		t.Fatalf("planned %d services, want 2: %v", len(seen), seen)
	}
	if seen[10] == seen[20] {
		t.Errorf("both inbounds tag subject 7 as %q, so Xray sums their traffic", seen[10])
	}
	for svcID, tag := range seen {
		gotSubject, gotService, err := parseSubjectEmail(tag)
		if err != nil {
			t.Errorf("service %d wrote tag %q, which accounting cannot parse: %v",
				svcID, tag, err)
			continue
		}
		if gotSubject != 7 || gotService != svcID {
			t.Errorf("tag %q parses to subject %d service %d, want 7 and %d",
				tag, gotSubject, gotService, svcID)
		}
	}
}

// statsRuntime reports a fixed set of counters, which is all Usage needs.
type statsRuntime struct {
	mockRuntime
}

// And the reading half: what the adapter reports back carries the service.
func TestUsageAttributesEachSampleToItsService(t *testing.T) {
	rt := &statsRuntime{}
	rt.setStats([]UserStat{
		{Email: subjectEmail(1, 10), Uplink: 100, Downlink: 200},
		{Email: subjectEmail(1, 20), Uplink: 5, Downlink: 7},
		{Email: subjectEmail(2, 10), Uplink: 1, Downlink: 2},
	})
	a := &Adapter{rt: rt, hotAdd: true, dir: t.TempDir()}

	samples, err := a.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("got %d samples, want 3: %+v", len(samples), samples)
	}

	type key struct{ subject, service int64 }
	got := map[key]adapter.UsageSample{}
	for _, s := range samples {
		got[key{s.SubjectID, s.ServiceID}] = s
	}

	// The same subject on two services must arrive as two attributed samples,
	// not one merged total.
	a10, ok := got[key{1, 10}]
	if !ok {
		t.Fatalf("no sample for subject 1 on service 10: %+v", samples)
	}
	if a10.UplinkBytes != 100 || a10.DownlinkBytes != 200 {
		t.Errorf("subject 1 / service 10 = %d up %d down, want 100 and 200",
			a10.UplinkBytes, a10.DownlinkBytes)
	}
	a20, ok := got[key{1, 20}]
	if !ok {
		t.Fatalf("no sample for subject 1 on service 20: %+v", samples)
	}
	if a20.UplinkBytes != 5 {
		t.Errorf("subject 1 / service 20 uplink = %d, want 5", a20.UplinkBytes)
	}
	if _, ok := got[key{2, 10}]; !ok {
		t.Errorf("no sample for subject 2 on service 10: %+v", samples)
	}
}

// A node still running the pre-C2 config reports the old tags until it next
// converges. That traffic is real and must arrive, unattributed rather than
// discarded.
func TestUsageFromLegacyTagsIsReportedUnattributed(t *testing.T) {
	rt := &statsRuntime{}
	rt.setStats([]UserStat{
		{Email: "subject-1@antimage", Uplink: 100, Downlink: 200},
	})
	a := &Adapter{rt: rt, hotAdd: true, dir: t.TempDir()}

	samples, err := a.Usage(context.Background())
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("got %d samples, want 1; an agent upgrade dropped the traffic "+
			"Xray was still counting against the old tags", len(samples))
	}
	if samples[0].SubjectID != 1 {
		t.Errorf("subject = %d, want 1", samples[0].SubjectID)
	}
	if samples[0].ServiceID != 0 {
		t.Errorf("service = %d, want 0; a legacy tag names no service and "+
			"inventing one would put a wrong number into a bill", samples[0].ServiceID)
	}
}
