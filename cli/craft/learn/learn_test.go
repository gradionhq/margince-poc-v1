package learn

import (
	"path/filepath"
	"testing"

	"github.com/gradionhq/margince/cli/craft/gate"
)

func TestStore_capturesAllFourSignalTypesWithProvenance(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "signals.jsonl"))
	want := []Signal{
		{Type: SignalAdjudication, Polarity: Negative, Provenance: "dispute:f1", Category: "over-commenting"},
		{Type: SignalSpotAudit, Polarity: Positive, Provenance: "audit:pr-42"},
		{Type: SignalPostMergeDefect, Polarity: Positive, Provenance: "defect:issue-99"},
		{Type: SignalResolvedFixPair, Polarity: Positive, Provenance: "fix:f1@x.go"},
	}
	for _, s := range want {
		if err := store.Append(s); err != nil {
			t.Fatalf("append %s: %v", s.Type, err)
		}
	}
	got, err := store.All()
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d signals, want %d", len(got), len(want))
	}
	seen := map[SignalType]bool{}
	for _, s := range got {
		seen[s.Type] = true
		if s.Provenance == "" {
			t.Errorf("signal %s lost provenance", s.Type)
		}
	}
	for _, ty := range []SignalType{SignalAdjudication, SignalSpotAudit, SignalPostMergeDefect, SignalResolvedFixPair} {
		if !seen[ty] {
			t.Errorf("signal type %s not captured", ty)
		}
	}
}

func TestStore_refusesSignalWithoutProvenance(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "signals.jsonl"))
	if err := store.Append(Signal{Type: SignalSpotAudit}); err == nil {
		t.Error("expected an error for a signal with no provenance")
	}
}

func TestDisputesFromMarkers_onlyDisputes(t *testing.T) {
	markers := []gate.Marker{
		{Kind: gate.KindFix, ID: "f1", File: "a.go", Line: 3},
		{Kind: gate.KindDispute, ID: "f2", File: "b.go", Line: 9, Reason: "any at serialization boundary"},
	}
	d := DisputesFromMarkers(markers)
	if len(d) != 1 || d[0].FindingID != "f2" || d[0].Status != StatusOpen {
		t.Fatalf("expected one open dispute for f2, got %+v", d)
	}
}

func TestResolve_bothOutcomes_neitherMerges(t *testing.T) {
	d := Dispute{FindingID: "f2", File: "b.go", Line: 9, Reason: "false positive", Status: StatusOpen}

	dismiss, err := Resolve(d, Dismiss)
	if err != nil {
		t.Fatal(err)
	}
	if dismiss.Dispute.Status != StatusDismissed {
		t.Errorf("dismiss status = %s", dismiss.Dispute.Status)
	}
	if dismiss.Signal.Polarity != Negative || dismiss.Signal.Type != SignalAdjudication {
		t.Errorf("dismiss signal = %+v, want negative adjudication", dismiss.Signal)
	}
	if dismiss.RevertToCraftFix {
		t.Error("a dismiss must not revert to CRAFT-FIX")
	}

	uphold, err := Resolve(d, Uphold)
	if err != nil {
		t.Fatal(err)
	}
	if uphold.Dispute.Status != StatusUpheld {
		t.Errorf("uphold status = %s", uphold.Dispute.Status)
	}
	if uphold.Signal.Polarity != Positive {
		t.Errorf("uphold signal polarity = %s, want positive", uphold.Signal.Polarity)
	}
	if !uphold.RevertToCraftFix {
		t.Error("an uphold must revert to CRAFT-FIX so the residue gate keeps blocking")
	}

	// Neither resolution carries a merge action — adjudication never merges the PR.
	// (Structural: Resolution has no merge field; the residue gate governs the merge.)
}
