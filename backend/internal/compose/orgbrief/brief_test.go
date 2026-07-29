// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// The rules that decide what a reader is shown, none of which need a
// database:
//
//   - the cache key changes when the ACCOUNT changes, and when the reader
//     changes, because a brief written for one reader is not true for
//     another;
//   - a sentence citing a record the input never carried is dropped, since
//     the citation is the only thing making the sentence checkable;
//   - a lane that fails produces the floor, not an error.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// briefOrgID is the account every fixture here is about.
const briefOrgID = "33333333-3333-4333-8333-333333333333"

func inputFixture() Input {
	return Input{
		Name: "Brandt Automotive GmbH", Industry: "Automotive",
		Strength: 41, ContactCount: 2,
		OpenDeals: []DealIn{{
			ID: "11111111-1111-4111-8111-111111111111", Name: "Fleet retrofit",
			Stage: "Proposal", AmountMinor: 4_800_000, Currency: "EUR", Stalled: true,
		}},
		Recent: []ActIn{{
			ID: "22222222-2222-4222-8222-222222222222", Kind: "email",
			Subject: "Re: proposal", At: "2026-07-10T09:00:00Z",
		}},
	}
}

func TestFingerprintTracksTheAccountNotTheRecordVersion(t *testing.T) {
	base := inputFixture()
	first, err := Fingerprint(base, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	again, err := Fingerprint(inputFixture(), "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if first != again {
		t.Error("the same account fingerprints differently twice — the cache would never hit")
	}

	// A deal moving stage touches no organization row, which is exactly why
	// the key is the input rather than that row's version.
	moved := inputFixture()
	moved.OpenDeals[0].Stage = "Negotiation"
	changed, err := Fingerprint(moved, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if changed == first {
		t.Error("a deal changing stage left the fingerprint alone — the cached brief would describe the old pipeline")
	}

	// Re-pointing the lane rewrites briefs rather than leaving text
	// attributed to a model that no longer writes it.
	rebound, err := Fingerprint(base, "routing-2")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if rebound == first {
		t.Error("re-pointing the model lane left the fingerprint alone")
	}
}

// Two readers of the same account with different grants must never share a
// cached brief: what one may read, the other may not.
func TestFingerprintSeparatesReadersWithDifferentGrants(t *testing.T) {
	full := inputFixture()
	restricted := inputFixture()
	restricted.OpenDeals = nil
	restricted.SectionsOmitted = []string{"deals"}

	a, err := Fingerprint(full, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	b, err := Fingerprint(restricted, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if a == b {
		t.Error("a reader who cannot see the deals shares a cache key with one who can")
	}
}

func TestParseBriefDropsSentencesCitingRecordsTheInputNeverCarried(t *testing.T) {
	in := inputFixture()
	reply := `{"sentences":[
	  {"text":"The retrofit deal has stalled.","evidence":[{"entity_type":"deal","entity_id":"11111111-1111-4111-8111-111111111111"}]},
	  {"text":"They are close to signing.","evidence":[{"entity_type":"deal","entity_id":"99999999-9999-4999-8999-999999999999"}]}
	]}`
	kept, err := ParseBrief(reply, briefOrgID, in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("kept %d sentences, want only the grounded one", len(kept))
	}
	if !strings.Contains(kept[0].Text, "stalled") {
		t.Errorf("kept the wrong sentence: %q", kept[0].Text)
	}
}

// The account itself is always citable: it is the record the brief is about.
func TestParseBriefKeepsASentenceCitingTheAccount(t *testing.T) {
	kept, err := ParseBrief(
		`{"sentences":[{"text":"An automotive supplier.","evidence":[{"entity_type":"organization","entity_id":"`+briefOrgID+`"}]}]}`,
		briefOrgID, inputFixture())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("kept %d sentences, want the one about the account", len(kept))
	}
}

type failingLane struct{}

func (failingLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("budget exhausted")
}

type nonsenseLane struct{}

func (nonsenseLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: "I'm afraid I can't do that."}, nil
}

func TestWriteFallsBackRatherThanFailing(t *testing.T) {
	in := inputFixture()
	orgID := briefOrgID

	for name, lane := range map[string]Completer{
		"no lane configured":   nil,
		"lane over budget":     failingLane{},
		"lane answering prose": nonsenseLane{},
	} {
		t.Run(name, func(t *testing.T) {
			sentences, by, err := Write(context.Background(), lane, orgID, in)
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if by != "deterministic" {
				t.Errorf("generated_by = %q, want deterministic — the reader must know which wrote it", by)
			}
			if len(sentences) == 0 {
				t.Fatal("no sentences: the floor must always produce a brief")
			}
			// The floor cites too, so the card behaves identically either way.
			if len(sentences[0].Evidence) == 0 {
				t.Error("a deterministic sentence carries no evidence")
			}
		})
	}
}

// The deterministic floor states what is on the page and nothing else — no
// inferred cause, no suggested next move.
func TestDeterministicNamesTheStalledDealAndTheLastTouch(t *testing.T) {
	sentences := Deterministic(briefOrgID, inputFixture())
	var all strings.Builder
	for _, sentence := range sentences {
		all.WriteString(sentence.Text)
		all.WriteString(" ")
	}
	text := all.String()
	if !strings.Contains(text, "Fleet retrofit") {
		t.Errorf("the stalled deal is not named: %q", text)
	}
	if !strings.Contains(text, "Re: proposal") {
		t.Errorf("the last contact is not named: %q", text)
	}
	if !strings.Contains(text, "41") || !strings.Contains(text, "2 known contact") {
		t.Errorf("the score is reported without the contact count it was taken over: %q", text)
	}
}

// A reply citing SOME organization is not a reply citing THIS one: an id the
// reader never saw would render as a link into a record their scope may hide.
func TestParseBriefRefusesACitationToAnotherAccount(t *testing.T) {
	kept, err := ParseBrief(
		`{"sentences":[{"text":"A promising account.","evidence":[{"entity_type":"organization","entity_id":"44444444-4444-4444-8444-444444444444"}]}]}`,
		briefOrgID, inputFixture())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kept) != 0 {
		t.Errorf("kept a sentence citing a different account: %+v", kept)
	}
}
