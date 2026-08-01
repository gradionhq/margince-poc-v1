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
	"slices"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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

	// An open task's due date rides the fingerprint, so the answer that names
	// it is rewritten when it moves. It is also what proves the cache turns
	// over across the shape change that added the field: the same three tasks
	// hash differently once they carry their dates, so no reader is served an
	// answer written before the writer knew them.
	undated := inputFixture()
	undated.OpenTasks = []TaskIn{{ID: "77777777-7777-4777-8777-777777777777", Name: "Send the paperwork"}}
	dated := inputFixture()
	dated.OpenTasks = []TaskIn{{
		ID: "77777777-7777-4777-8777-777777777777", Name: "Send the paperwork",
		Due: "2026-07-21T09:00:00Z",
	}}
	withoutDue, err := Fingerprint(undated, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	withDue, err := Fingerprint(dated, "routing-1")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if withoutDue == withDue {
		t.Error("a task's due date left the fingerprint alone — the cached answer would name no date")
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

// Some models wrap JSON in a ```json fence. Every other model-reply parser in
// the tree reduces through ai.Unfence first; this one must too, or a provider
// that fences loses the whole model lane to the deterministic floor and the
// reader never learns why.
func TestParseBriefReadsAFencedReply(t *testing.T) {
	in := inputFixture()
	fenced := "```json\n" +
		`{"sentences":[{"text":"The retrofit deal has stalled.","evidence":[{"entity_type":"deal","entity_id":"11111111-1111-4111-8111-111111111111"}]}]}` +
		"\n```"

	kept, err := ParseBrief(fenced, briefOrgID, in)
	if err != nil {
		t.Fatalf("a fenced reply must parse: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("kept %d sentences, want the one grounded sentence", len(kept))
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

// A record id in the prose is developer output, whatever the sentence says
// around it, and the whole sentence goes: the id sits mid-clause, so cutting it
// out leaves grammar the reader has to decode. The grounded sentence beside it
// survives, which is what makes this a filter rather than a kill switch.
func TestParseBriefDropsASentenceThatSpellsAnIDAtTheReader(t *testing.T) {
	in := inputFixture()
	dealID := in.OpenDeals[0].ID
	kept, err := ParseBrief(`{"sentences":[
	  {"text":"The retrofit deal (ID: `+dealID+`) has stalled.","evidence":[{"entity_type":"deal","entity_id":"`+dealID+`"}]},
	  {"text":"The retrofit deal has stalled.","evidence":[{"entity_type":"deal","entity_id":"`+dealID+`"}]}
	]}`, briefOrgID, in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("kept %d sentences, want only the one written for a reader: %+v", len(kept), kept)
	}
	if strings.Contains(kept[0].Text, dealID) {
		t.Errorf("the surviving sentence spells the id: %q", kept[0].Text)
	}
}

// The same record cited twice renders as two identical chips that go to the
// same place. They collapse to one, in the order the reply cited them, so the
// citation the sentence leads with stays the one the reader sees first.
func TestParseBriefCollapsesRepeatedCitations(t *testing.T) {
	in := inputFixture()
	dealID, actID := in.OpenDeals[0].ID, in.Recent[0].ID
	kept, err := ParseBrief(`{"sentences":[{"text":"The retrofit stalled after the last mail.","evidence":[
	  {"entity_type":"deal","entity_id":"`+dealID+`"},
	  {"entity_type":"activity","entity_id":"`+actID+`"},
	  {"entity_type":"deal","entity_id":"`+dealID+`"}]}]}`, briefOrgID, in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("kept %d sentences, want the one grounded sentence", len(kept))
	}
	want := []Evidence{
		{EntityType: citeDeal, EntityID: dealID},
		{EntityType: citeActivity, EntityID: actID},
	}
	if !slices.Equal(kept[0].Evidence, want) {
		t.Errorf("evidence = %+v, want the two distinct records in the order cited: %+v", kept[0].Evidence, want)
	}
}

type failingLane struct{}

func (failingLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("budget exhausted")
}

// scriptedLane answers with whatever the case wrote for it.
type scriptedLane struct{ reply string }

func (l *scriptedLane) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{Text: l.reply}, nil
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

// A citation is a TYPE and an id, and both must match. Keying on the id alone
// accepted a real deal id cited as a person, which routes the reader to the
// wrong screen — or to a record of a kind they were never shown.
func TestParseBriefRefusesARealIDUnderTheWrongType(t *testing.T) {
	in := inputFixture()
	dealID := in.OpenDeals[0].ID
	kept, err := ParseBrief(
		`{"sentences":[{"text":"Dana is the champion.","evidence":[{"entity_type":"person","entity_id":"`+dealID+`"}]}]}`,
		briefOrgID, in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kept) != 0 {
		t.Errorf("kept a sentence citing a deal id as a person: %+v", kept)
	}
}

// One invented citation drops the WHOLE sentence. A sentence resting partly
// on a record that does not exist is not made checkable by the half that
// does — keeping it with the good citation attached would present it as
// checked when it is not.
func TestParseBriefDropsASentenceWithAnyUngroundedCitation(t *testing.T) {
	in := inputFixture()
	kept, err := ParseBrief(
		`{"sentences":[{"text":"The retrofit stalled after the buyer left.","evidence":[
		  {"entity_type":"deal","entity_id":"`+in.OpenDeals[0].ID+`"},
		  {"entity_type":"person","entity_id":"55555555-5555-4555-8555-555555555555"}]}]}`,
		briefOrgID, in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kept) != 0 {
		t.Errorf("kept a sentence resting on a record that does not exist: %+v", kept)
	}
}

// A contact or an open task can be written about, so both must be citable —
// the prompt invites a person citation, and an input that could not ground one
// meant the sentence was silently dropped and the reader lost a true fact.
func TestParseBriefGroundsContactsAndTasks(t *testing.T) {
	in := inputFixture()
	in.Contacts = []NamedIn{{ID: "66666666-6666-4666-8666-666666666666", Name: "Dana Buyer"}}
	in.OpenTasks = []TaskIn{{ID: "77777777-7777-4777-8777-777777777777", Name: "Send the paperwork"}}

	kept, err := ParseBrief(
		`{"sentences":[
		  {"text":"Dana Buyer is your contact.","evidence":[{"entity_type":"person","entity_id":"`+in.Contacts[0].ID+`"}]},
		  {"text":"One task is open.","evidence":[{"entity_type":"activity","entity_id":"`+in.OpenTasks[0].ID+`"}]}]}`,
		briefOrgID, in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(kept) != 2 {
		t.Errorf("kept %d sentences, want both the contact and the task grounded", len(kept))
	}
}

// The brief answers two questions, in this order: where we stand with this
// account, and what the company is. The second half exists because the
// company page's profile card — sixteen scraped fields, every value a
// paragraph — is not something a rep reads before a call.

func TestDeterministicClosesWithWhatTheCompanyIs(t *testing.T) {
	in := Input{
		Name: "ScaleCommerce",
		Profile: []ProfileIn{
			{Field: "offer_summary", Value: "Managed hosting for e-commerce"},
			{Field: "icp", Value: "Shop operators and agencies"},
		},
	}
	sentences := Deterministic("org-1", in)
	if len(sentences) < 3 {
		t.Fatalf("sentences = %+v, want the identity line plus both profile lines", sentences)
	}
	last := sentences[len(sentences)-2:]
	if !strings.Contains(last[0].Text, "Managed hosting for e-commerce") {
		t.Errorf("first profile line = %q, want the stored statement verbatim", last[0].Text)
	}
	if !strings.Contains(last[1].Text, "Shop operators and agencies") {
		t.Errorf("second profile line = %q, want the stored statement verbatim", last[1].Text)
	}
	// The company half is about the company, so it cites the organization.
	for _, sentence := range last {
		if len(sentence.Evidence) != 1 || sentence.Evidence[0].EntityType != citeOrganization {
			t.Errorf("profile line %q cites %+v, want the organization", sentence.Text, sentence.Evidence)
		}
	}
}

// A profile the page has not gathered yet is not a gap to apologize for.
func TestDeterministicSaysNothingAboutACompanyItKnowsNothingAbout(t *testing.T) {
	for _, sentence := range Deterministic("org-1", Input{Name: "Acme"}) {
		for label := range profileLabels {
			if strings.Contains(sentence.Text, label) {
				t.Errorf("sentence %q talks about the company with no profile to talk from", sentence.Text)
			}
		}
	}
}

// Eight statements is the profile card, which the reader can open underneath.
func TestDeterministicKeepsTheCompanyHalfShort(t *testing.T) {
	in := Input{Name: "Acme"}
	for _, field := range briefProfileFields {
		in.Profile = append(in.Profile, ProfileIn{Field: field, Value: "something about " + field})
	}
	var profileLines int
	for _, sentence := range Deterministic("org-1", in) {
		if strings.HasPrefix(sentence.Text, "What ") ||
			strings.HasPrefix(sentence.Text, "Who ") ||
			strings.HasPrefix(sentence.Text, "How ") {
			profileLines++
		}
	}
	if profileLines > deterministicProfileLines {
		t.Errorf("profile lines = %d, want at most %d", profileLines, deterministicProfileLines)
	}
}

func TestFoldProfileTakesAFixedOrderAndDropsTheEmpty(t *testing.T) {
	var in Input
	in.foldProfile([]crmcontracts.CompanyProfileField{
		{Field: "icp", Value: "Agencies"},
		{Field: "offer_summary", Value: "Hosting"},
		{Field: "usp", Value: "   "},
		// Not in the brief's subset: registry detail describes a legal
		// entity, not a business.
		{Field: "register_vat", Value: "DE123"},
	})
	if len(in.Profile) != 2 {
		t.Fatalf("profile = %+v, want the two business statements only", in.Profile)
	}
	// Fixed order, so the same account fingerprints the same way whatever
	// order the store returned its rows in.
	if in.Profile[0].Field != "offer_summary" || in.Profile[1].Field != "icp" {
		t.Errorf("profile order = %+v, want offer_summary then icp", in.Profile)
	}
}

func TestFoldProfileBoundsOneStatement(t *testing.T) {
	var in Input
	in.foldProfile([]crmcontracts.CompanyProfileField{
		{Field: "offer_summary", Value: strings.Repeat("x", briefProfileValueMax*3)},
	})
	if got := len(in.Profile[0].Value); got != briefProfileValueMax {
		t.Errorf("value length = %d, want it bounded to %d", got, briefProfileValueMax)
	}
}

// The model writes the relationship half; the company half is quoted from
// statements a human already accepted. Putting curated prose through a model
// buys nothing and risks a paraphrase nobody checked — so both writers close
// with the SAME sentences, and certification stays valid because the prompt
// never changed.
func TestModelBriefClosesWithTheSameQuotedCompanyLines(t *testing.T) {
	in := inputFixture()
	in.Profile = []ProfileIn{{Field: "offer_summary", Value: "Managed hosting"}}
	lane := &scriptedLane{reply: `{"sentences":[{"text":"Two deals are open.","evidence":[{"entity_type":"organization","entity_id":"` + briefOrgID + `"}]}]}`}

	written, by, err := Write(context.Background(), lane, briefOrgID, in)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if by != crmcontracts.Model {
		t.Fatalf("generated_by = %q, want the model path", by)
	}
	last := written[len(written)-1]
	if !strings.Contains(last.Text, "Managed hosting") {
		t.Errorf("last sentence = %q, want the company statement quoted", last.Text)
	}
	if len(last.Evidence) != 1 || last.Evidence[0].EntityType != citeOrganization {
		t.Errorf("company line cites %+v, want the organization", last.Evidence)
	}
}
