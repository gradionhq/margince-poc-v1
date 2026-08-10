// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentquota

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// proposalPassport is one credential's id: the proposals here are about a
// window, and a window belongs to exactly one of them.
var proposalPassport = ids.New[ids.PassportKind]().UUID

// The proposal is the question a human answers, so what it carries is what they
// are answering ABOUT. Stored rather than re-read at decision time: re-reading
// the counter would let the number move between the asking and the answering.
func TestAProposalCarriesTheQuestionItWasAskedFrom(t *testing.T) {
	reading := Reading{Counter: Reads, Observed: 2431, Limit: 2000, Exceeded: true, Bucket: 42}

	p := NewReleaseProposal(reading, proposalPassport, "search_records")

	if p.Counter != Reads || p.Observed != 2431 || p.Limit != 2000 || p.Tool != "search_records" {
		t.Errorf("the proposal reads %+v; it must carry the reading it was built from", p)
	}
	window, err := p.Window()
	if err != nil || window != 42 {
		t.Errorf("the proposal names window (%d, %v), want 42", window, err)
	}
}

// A round trip through the stored payload changes nothing. It is the one thing
// this type exists for: two modules read it, and neither may import the other.
func TestAProposalSurvivesTheStoredPayloadUnchanged(t *testing.T) {
	p := NewReleaseProposal(Reading{Counter: Writes, Observed: 240, Limit: 200, Bucket: 9}, proposalPassport, "update_record")
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	back, err := DecodeReleaseProposal(raw)
	if err != nil {
		t.Fatal(err)
	}

	if back != p {
		t.Errorf("a stored proposal read back as %+v, want %+v", back, p)
	}
}

// The decode is a VALIDATION, because the payload it reads is editable: the
// modify-then-approve arm pins entity references, and this payload has none. A
// counter no rung of the ladder can release is refused here rather than at the
// meter, so a human's approval never appears to grant something it cannot.
func TestDecodingRefusesAPayloadThatNamesAnUnreleasableQuota(t *testing.T) {
	for _, counter := range []Counter{Egress, Calls, Cost, "invented"} {
		raw, err := json.Marshal(ReleaseProposal{Counter: counter, Bucket: "1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeReleaseProposal(raw); err == nil {
			t.Errorf("a staged payload naming %q was accepted", counter)
		}
	}
	if _, err := DecodeReleaseProposal(json.RawMessage(`not json`)); err == nil {
		t.Error("a payload that is not JSON was accepted as a proposal")
	}
}

// A window that is not a window names nothing, and answers so. Picking the
// CURRENT window for it would widen a window nobody was shown — the exact
// failure the stored bucket exists to prevent.
func TestAnUnreadableWindowNamesNothing(t *testing.T) {
	for _, bucket := range []string{"", "not-a-window", "9e9", "12.5"} {
		if _, err := (ReleaseProposal{Counter: Reads, Bucket: bucket}).Window(); err == nil {
			t.Errorf("window %q was read as a window", bucket)
		}
	}
}

// The identity is what makes one crossed window ONE question. Without it an
// agent looping on a refusal stages a row per call, and the human answering the
// third of forty has released the window with thirty-seven left to dismiss.
//
// The tool is deliberately absent from it: the question is about the counter,
// and answering it for one tool answers it for all of them.
func TestOneCrossedWindowIsOneQuestionWhateverToolHitIt(t *testing.T) {
	reading := Reading{Counter: Reads, Observed: 2431, Limit: 2000, Bucket: 42}
	first, err := NewReleaseProposal(reading, proposalPassport, "search_records").Identity()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewReleaseProposal(reading, proposalPassport, "list_records").Identity()
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Errorf("two tools crossing one window ask two questions: %s vs %s", first, second)
	}

	// A different window, or a different counter, IS a different question.
	later, err := NewReleaseProposal(Reading{Counter: Reads, Bucket: 43}, proposalPassport, "search_records").Identity()
	if err != nil {
		t.Fatal(err)
	}
	if string(later) == string(first) {
		t.Error("a crossing in the next window joined the previous window's question")
	}
	writes, err := NewReleaseProposal(Reading{Counter: Writes, Bucket: 42}, proposalPassport, "update_record").Identity()
	if err != nil {
		t.Fatal(err)
	}
	if string(writes) == string(first) {
		t.Error("a write crossing joined the read crossing's question")
	}

	// And the collision that matters most: two agents lent by two different
	// humans cross the same counter in the same window constantly. If their
	// questions were one question, whichever lender answered would release only
	// their own passport's window and the other agent would be left refused
	// with nothing anybody can see.
	other, err := NewReleaseProposal(reading, ids.New[ids.PassportKind]().UUID, "search_records").Identity()
	if err != nil {
		t.Fatal(err)
	}
	if string(other) == string(first) {
		t.Error("two passports crossing one counter in one window ask a single question")
	}
}

// The identity has to be CONTAINED in the payload — the approvals engine
// matches them by jsonb containment, so a member spelled differently on either
// side silently stops deduplicating and the inbox fills up again.
func TestTheIdentityIsContainedInTheStoredPayload(t *testing.T) {
	p := NewReleaseProposal(Reading{Counter: Reads, Observed: 1, Limit: 2, Bucket: 7}, proposalPassport, "search_records")
	identity, err := p.Identity()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var stored, want map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(identity, &want); err != nil {
		t.Fatal(err)
	}
	for key, value := range want {
		if stored[key] != value {
			t.Errorf("identity member %q is %#v in the identity and %#v in the payload; containment will not match",
				key, value, stored[key])
		}
	}
}

// A soft counter with no ceiling composed is not a counter that has run out.
// The distinction matters because the only thing cost does is warn, and a
// warning raised whenever the ceiling is unknown is one nobody reads.
func TestASoftCounterWithNoCeilingReportsNoCeilingRatherThanNoHeadroom(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)

	reading := meter.Read(agentCtx(t), Cost)

	if reading.Exceeded {
		t.Error("a cost counter with no ceiling composed reported itself spent")
	}
	if reading.Limit != 0 {
		t.Errorf("a cost counter with no ceiling reported limit %d", reading.Limit)
	}
	if meter.CostShare(agentCtx(t)).Exceeded {
		t.Error("CostShare disagreed with Read about the same counter")
	}
}

// fixedCeiling is a workspace share that is already decided.
type fixedCeiling int

func (c fixedCeiling) TokensPerPassport(context.Context) int { return int(c) }

// And with a ceiling but no reachable counter it still says nothing. Cost is
// the ONE counter that does not fail closed, because failing closed on a
// warning means warning on every call while Redis is down.
func TestASoftCounterDoesNotFailClosed(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow).WithCostCeiling(fixedCeiling(40_000))

	reading := meter.Read(agentCtx(t), Cost)

	if reading.Exceeded {
		t.Error("an unreachable cost counter warned anyway; the warning stops meaning anything")
	}
	if reading.Limit != 40_000 {
		t.Errorf("the share reads %d, want the composed 40000", reading.Limit)
	}
}
