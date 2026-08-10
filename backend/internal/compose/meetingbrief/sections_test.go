// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/claims"
	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

const (
	meetingID  = "0198f000-0000-7000-8000-000000000001"
	dealID     = "0198f000-0000-7000-8000-000000000002"
	personID   = "0198f000-0000-7000-8000-000000000003"
	activityID = "0198f000-0000-7000-8000-000000000004"
)

func at(day int) time.Time {
	return time.Date(2026, time.August, day, 9, 0, 0, 0, time.UTC)
}

func ptr[T any](v T) *T { return &v }

// fullInput is a meeting with everything a brief could be written from, so a
// test that asserts one section's absence is asserting that section's own rule
// rather than an empty fixture.
func fullInput() Input {
	touched := at(4)
	return Input{
		ActivityID:  meetingID,
		Subject:     "Q3 review",
		StartsAt:    at(12),
		Now:         at(10),
		Company:     "Northwind",
		LastTouchAt: &touched,
		Deal: &DealIn{
			ID: dealID, Name: "Northwind platform", Stage: "Proposal",
			AmountMinor: 9500000, Currency: "EUR", CloseDate: ptr(at(30)),
		},
		Attendees: []AttendeeIn{
			{PersonID: personID, FullName: "Ana Roth", Title: "CFO", DealRole: "economic_buyer", LastTouch: &touched},
		},
		Commitments: []ClaimIn{{
			PersonName: "Ana Roth", Kind: kindCommitmentOurs, Body: "send the security pack",
			Status:   statusOpen,
			SourceID: activityID, SourceLabel: "Re: security review", DueAt: ptr(at(8)),
		}},
		Recent: []ActIn{{ID: activityID, Kind: "email", Subject: "Re: security review", Direction: "inbound", At: touched}},
	}
}

func sectionOf(t *testing.T, sections []Section, kind crmcontracts.MeetingBriefSectionKind) Section {
	t.Helper()
	for _, section := range sections {
		if section.Kind == kind {
			return section
		}
	}
	t.Fatalf("no %s section was written", kind)
	return Section{}
}

// The order is the spec's, not a rendering choice: goal and commitments lead
// because burying the ask is the canonical prep failure, and company context is
// last because it is background.
func TestSectionsAreWrittenInTheSpecsFixedOrder(t *testing.T) {
	want := []crmcontracts.MeetingBriefSectionKind{
		crmcontracts.MeetingBriefSectionKindHeader,
		crmcontracts.MeetingBriefSectionKindGoal,
		crmcontracts.MeetingBriefSectionKindAttendees,
		crmcontracts.MeetingBriefSectionKindCommitments,
		crmcontracts.MeetingBriefSectionKindDealState,
		crmcontracts.MeetingBriefSectionKindRisks,
		crmcontracts.MeetingBriefSectionKindTalkingPoints,
		crmcontracts.MeetingBriefSectionKindCompanyContext,
	}
	got := Deterministic(fullInput())
	if len(got) != len(want) {
		t.Fatalf("got %d sections, want the eight of ADR-0097 D5", len(got))
	}
	for i, kind := range want {
		if got[i].Kind != kind {
			t.Errorf("section %d is %s, want %s", i, got[i].Kind, kind)
		}
	}
}

// Every sentence cites a record. A brief line nobody can check against a record
// is the thing the grounding rule exists to prevent, and the floor is held to
// it exactly as a model writer would be.
func TestEverySentenceCitesARecordAndSpellsNoIDInProse(t *testing.T) {
	for _, section := range Deterministic(fullInput()) {
		for _, sentence := range section.Sentences {
			if len(sentence.Evidence) == 0 {
				t.Errorf("%s: uncited sentence %q", section.Kind, sentence.Text)
			}
			if claims.SpellsRecordID(sentence.Text) {
				t.Errorf("%s: sentence pastes a record id into prose: %q", section.Kind, sentence.Text)
			}
		}
	}
}

// The spec says risks is omitted when empty, and the wire filter drops any
// section left with nothing. A risks heading over nothing reads as "we looked
// and found none", which this floor cannot claim.
func TestRisksIsAbsentWhenNothingInTheRecordIsWrong(t *testing.T) {
	in := fullInput()
	// The one commitment is not yet due, so nothing is overdue and nothing was
	// objected to.
	in.Commitments[0].DueAt = ptr(at(20))
	for _, section := range wireSections(Deterministic(in)) {
		if section.Kind == crmcontracts.MeetingBriefSectionKindRisks {
			t.Fatalf("risks was rendered with %d sentences when the record holds no watch-out", len(section.Sentences))
		}
	}
}

func TestAnOverdueCommitmentOfOursBecomesARiskLabelledAsAnAssessment(t *testing.T) {
	risks := sectionOf(t, Deterministic(fullInput()), crmcontracts.MeetingBriefSectionKindRisks)
	if len(risks.Sentences) != 1 {
		t.Fatalf("got %d risks, want the one overdue promise", len(risks.Sentences))
	}
	if risks.Sentences[0].Nature != natureAssessment {
		t.Errorf("a risk read out of a record is nature %q, want %q", risks.Sentences[0].Nature, natureAssessment)
	}
}

// An attendee we have never exchanged anything with is flagged in WORDS.
// Walking in without knowing a decision-maker has never heard from you is the
// failure the section exists to prevent, so it cannot live in a badge the prose
// does not mention.
func TestAFirstTimeAttendeeIsFlaggedInTheProse(t *testing.T) {
	in := fullInput()
	in.Attendees[0].LastTouch = nil
	in.Attendees[0].FirstTime = true
	attendees := sectionOf(t, Deterministic(in), crmcontracts.MeetingBriefSectionKindAttendees)
	if len(attendees.Sentences) != 1 {
		t.Fatalf("got %d attendee lines, want one", len(attendees.Sentences))
	}
	const want = "Ana Roth, CFO, economic buyer — first time you are meeting them."
	if attendees.Sentences[0].Text != want {
		t.Errorf("attendee line is %q, want %q", attendees.Sentences[0].Text, want)
	}
}

// The goal leads with the ask the RECORD supports. With an open question on the
// table, answering it is the ask; a goal invented from nothing would be the
// external-context filler the spec's first hard rule forbids.
func TestTheGoalIsTheOpenQuestionWhenThereIsOne(t *testing.T) {
	in := fullInput()
	in.Commitments = append(in.Commitments, ClaimIn{
		PersonName: "Ana Roth", Kind: kindOpenQuestion, Body: "who signs the DPA",
		Status: statusOpen, SourceID: activityID,
	})
	goal := sectionOf(t, Deterministic(in), crmcontracts.MeetingBriefSectionKindGoal)
	if len(goal.Sentences) != 1 {
		t.Fatalf("got %d goal sentences, want the one ask", len(goal.Sentences))
	}
	const want = "Answer the open question from Ana Roth: who signs the DPA"
	if goal.Sentences[0].Text != want {
		t.Errorf("goal is %q, want %q", goal.Sentences[0].Text, want)
	}
}

// A dismissed claim is one a human said was never true. Resurrecting it in prep
// would put the correction in front of the person it was wrong about.
func TestADismissedClaimNeverReachesTheBrief(t *testing.T) {
	folded := foldClaims("Ana Roth", []crmcontracts.ConversationClaim{{
		Kind:   crmcontracts.CommitmentTheirs,
		Body:   "they will introduce us to procurement",
		Status: crmcontracts.ConversationClaimStatusDismissed,
	}})
	if len(folded) != 0 {
		t.Fatalf("a dismissed claim reached the brief: %+v", folded)
	}
}

// A malformed citation drops the WHOLE sentence, and a section left with
// nothing is omitted rather than rendered empty — which is what the contract's
// minItems promises a renderer.
func TestASentenceWithAnUnparseableCitationIsDroppedWholeWithItsSection(t *testing.T) {
	got := wireSections([]Section{{
		Kind:      crmcontracts.MeetingBriefSectionKindGoal,
		Sentences: []Sentence{{Text: "Close it out.", Evidence: []Evidence{{EntityType: citeDeal, EntityID: "not-an-id"}}}},
	}})
	if len(got) != 0 {
		t.Fatalf("a sentence citing an unparseable id was rendered: %+v", got)
	}
}

// Days, not a timestamp: the reader is deciding whether to open with an
// apology, and a date makes them do the arithmetic.
func TestTheHeaderSaysHowManyDaysTheRoomHasBeenQuiet(t *testing.T) {
	header := sectionOf(t, Deterministic(fullInput()), crmcontracts.MeetingBriefSectionKindHeader)
	last := header.Sentences[len(header.Sentences)-1].Text
	if last != "Last touch was 6 days ago." {
		t.Errorf("last-touch line is %q, want the day count", last)
	}
}

// Nothing here is cached, so a brief describing a room nobody has ever spoken
// to still renders its header rather than blanking.
func TestAColdRoomStillGetsAHeader(t *testing.T) {
	in := Input{ActivityID: meetingID, Subject: "Intro call", StartsAt: at(12), Now: at(10)}
	header := sectionOf(t, Deterministic(in), crmcontracts.MeetingBriefSectionKindHeader)
	if len(header.Sentences) != 2 {
		t.Fatalf("got %d header sentences, want the meeting line and the quiet line", len(header.Sentences))
	}
	if header.Sentences[1].Text != "Nothing has been captured with anyone in this room before." {
		t.Errorf("quiet line is %q", header.Sentences[1].Text)
	}
}
