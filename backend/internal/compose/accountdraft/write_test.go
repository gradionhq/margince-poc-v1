// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

// What the draft may and may not do with what it is given.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// scriptedLane answers one canned response and records what it was asked.
type scriptedLane struct {
	answer string
	err    error
	seen   []model.Request
}

func (l *scriptedLane) Complete(_ context.Context, req model.Request) (model.Response, error) {
	l.seen = append(l.seen, req)
	if l.err != nil {
		return model.Response{}, l.err
	}
	return model.Response{Text: l.answer}, nil
}

func sampleInput() Input {
	return Input{
		Company: "Glazed Frog GmbH",
		Recipient: RecipientIn{
			ID:        "019fe7ae-0000-7000-8000-000000000001",
			Name:      "Sarah Cole",
			FirstName: "Sarah",
			Email:     "sarah@glazedfrog.example",
			Bucket:    "warm",
		},
		Deal: &DealIn{
			ID: "019fe7ae-0000-7000-8000-000000000002", Name: "Expansion Phase 2",
		},
	}
}

// DRAFT-AC-N-4: the reasons travel in their own field. A body that explains
// itself is a body the rep has to edit before sending, and the composer needs
// the parts rather than the prose.
func TestTheReasonsNeverAppearInTheBody(t *testing.T) {
	answer := `{"subject":"Next steps","body":"Hi Sarah,\n\nShall we pick this up?",
	  "reasoning":[{"kind":"deal","label":"expansion offer","entity_type":"deal",
	  "entity_id":"019fe7ae-0000-7000-8000-000000000002"}]}`
	lane := &scriptedLane{answer: answer}

	draft, by, err := Write(context.Background(), lane, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if by != crmcontracts.Model {
		t.Fatalf("generated_by = %q, want model", by)
	}
	if len(draft.Reasoning) != 1 || draft.Reasoning[0].Label != "expansion offer" {
		t.Fatalf("reasoning = %+v, want the one grounded reason", draft.Reasoning)
	}
	if strings.Contains(strings.ToLower(draft.Body), "expansion offer") {
		t.Fatalf("the reason leaked into the body: %q", draft.Body)
	}
}

// A citation the caller's own 360 did not carry is either invented or outside
// their row scope. Either way the chip would open nothing, so the reason is
// dropped rather than shown.
func TestAReasonCitingARecordTheReaderCannotSeeIsDropped(t *testing.T) {
	answer := `{"subject":"S","body":"B","reasoning":[
	  {"kind":"deal","label":"a deal you cannot see","entity_type":"deal",
	   "entity_id":"019fe7ae-0000-7000-8000-0000000000ff"},
	  {"kind":"intent","label":"shorter"}]}`
	draft, _, err := Write(context.Background(), &scriptedLane{answer: answer}, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Reasoning) != 1 {
		t.Fatalf("reasoning = %+v, want only the uncited intent", draft.Reasoning)
	}
	if draft.Reasoning[0].Kind != crmcontracts.AccountDraftReasonKindIntent {
		t.Fatalf("kept %q, want the intent — a reason with no citation is still checkable",
			draft.Reasoning[0].Kind)
	}
}

// A citation is a PAIR. An id checked without its type lets a deal id come
// back labelled as a person, and the chip then opens the wrong record's page
// rather than nothing — the worse failure, because it looks like it worked.
func TestAReasonCitingTheRightIdAsTheWrongKindIsDropped(t *testing.T) {
	answer := `{"subject":"S","body":"B","reasoning":[
	  {"kind":"deal","label":"mislabelled","entity_type":"person",
	   "entity_id":"019fe7ae-0000-7000-8000-000000000002"}]}`
	draft, _, err := Write(context.Background(), &scriptedLane{answer: answer}, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Reasoning) != 0 {
		t.Fatalf("reasoning = %+v, want none: the deal was cited as a person", draft.Reasoning)
	}
}

// Only the caller's own intent may cite nothing. An uncited "deal" reason is
// a claim about a record with no record behind it.
func TestAnUncitedReasonSurvivesOnlyForTheCallersOwnIntent(t *testing.T) {
	answer := `{"subject":"S","body":"B","reasoning":[
	  {"kind":"deal","label":"something about a deal"},
	  {"kind":"dossier","label":"something about the company"},
	  {"kind":"intent","label":"shorter"}]}`
	draft, _, err := Write(context.Background(), &scriptedLane{answer: answer}, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Reasoning) != 1 ||
		draft.Reasoning[0].Kind != crmcontracts.AccountDraftReasonKindIntent {
		t.Fatalf("reasoning = %+v, want only the intent", draft.Reasoning)
	}
}

// A kind outside the contract's closed vocabulary would render as an
// unlabelled chip, because the composer groups reasons by kind.
func TestAnUnknownReasonKindIsDropped(t *testing.T) {
	answer := `{"subject":"S","body":"B","reasoning":[{"kind":"vibes","label":"a feeling"}]}`
	draft, _, err := Write(context.Background(), &scriptedLane{answer: answer}, sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Reasoning) != 0 {
		t.Fatalf("reasoning = %+v, want none", draft.Reasoning)
	}
}

// A model that is down, over budget or answering nonsense must not cost the
// rep their draft: the floor is a real message they can edit, and
// generated_by says which writer produced it.
func TestAFailedLaneDegradesToTheFloorRatherThanErroring(t *testing.T) {
	for name, lane := range map[string]*scriptedLane{
		"nonsense": {answer: "not json at all"},
		"empty":    {answer: `{"subject":"","body":""}`},
	} {
		t.Run(name, func(t *testing.T) {
			draft, by, err := Write(context.Background(), lane, sampleInput())
			if err != nil {
				t.Fatalf("a bad answer must degrade, not error: %v", err)
			}
			if by != crmcontracts.Deterministic {
				t.Fatalf("generated_by = %q, want deterministic", by)
			}
			if draft.Subject == "" || draft.Body == "" {
				t.Fatal("the floor produced no message")
			}
		})
	}
}

// The account's own text — a contact's name, a deal's name — is written by
// people outside this workspace's control. It is quoted inside a one-time
// boundary rather than concatenated into the instructions.
func TestTheAccountSummaryTravelsInsideAFence(t *testing.T) {
	lane := &scriptedLane{answer: `{"subject":"S","body":"B"}`}
	if _, _, err := Write(context.Background(), lane, sampleInput()); err != nil {
		t.Fatal(err)
	}
	req := lane.seen[0]
	marker, ok := promptfence.MarkerIn(req.System)
	if !ok {
		t.Fatal("the system prompt declares no data boundary")
	}
	content := req.Messages[0].Content
	if !strings.Contains(content, marker) {
		t.Fatalf("the account summary is outside the boundary the prompt declared")
	}
}

// The caller's own instruction is the one input NOT fenced: fencing a
// person's own words would tell the model to treat the reader as an attacker.
func TestTheCallersOwnIntentIsOutsideTheFence(t *testing.T) {
	in := sampleInput()
	in.Intent = "keep it short"
	lane := &scriptedLane{answer: `{"subject":"S","body":"B"}`}
	if _, _, err := Write(context.Background(), lane, in); err != nil {
		t.Fatal(err)
	}
	content := lane.seen[0].Messages[0].Content
	marker, _ := promptfence.MarkerIn(lane.seen[0].System)
	// The payload is open-marker, summary, close-marker, then the intent. So
	// the text after the LAST marker is what sits outside the span.
	last := strings.LastIndex(content, marker)
	if last < 0 {
		t.Fatal("no boundary in the payload")
	}
	if !strings.Contains(content[last+len(marker):], "keep it short") {
		t.Fatalf("the caller's intent was fenced with the untrusted data: %q", content)
	}
	// And it is not inside the fenced JSON either.
	first := strings.Index(content, marker)
	fenced := content[first+len(marker) : last]
	var payload map[string]any
	if err := json.Unmarshal([]byte(fenced), &payload); err == nil {
		if _, present := payload["intent"]; present {
			t.Fatal("the intent was also serialised into the fenced summary")
		}
	}
}

// DRAFT-AC-N-2, as far as a unit test can see it: the package holds no
// database handle and no writer. The structural half of the guarantee is that
// Service has no pool field — see service.go — and this pins the behavioural
// half: nothing the lane answers can make the draft persist anything, because
// there is nothing to persist through.
func TestDraftingNeverReturnsADraftRef(t *testing.T) {
	// draft_ref exists so a served draft can be scored later, which is a
	// write. This operation performs none, so the field stays null.
	out := wire(Deterministic(sampleInput()), crmcontracts.Deterministic)
	if out.DraftRef != nil {
		t.Fatalf("draft_ref = %v, want null: recording a served draft is a write", out.DraftRef)
	}
}
