// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package meetingbrief

import (
	"strings"
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
	projectID  = "0198f000-0000-7000-8000-000000000005"
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
		crmcontracts.MeetingBriefSectionKindWhatChanged,
		crmcontracts.MeetingBriefSectionKindAttendees,
		crmcontracts.MeetingBriefSectionKindCommitments,
		crmcontracts.MeetingBriefSectionKindDealState,
		crmcontracts.MeetingBriefSectionKindRisks,
		crmcontracts.MeetingBriefSectionKindTalkingPoints,
		crmcontracts.MeetingBriefSectionKindCompanyContext,
	}
	got := Deterministic(fullInput())
	if len(got) != len(want) {
		t.Fatalf("got %d sections, want the eight of ADR-0097 D5 plus what_changed", len(got))
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

// One claim, one home. The sharpest overdue promise of ours IS the goal,
// dated; it is not said again as a risk. A second overdue promise, which the
// goal did not take, is the risk — labelled as the assessment it is.
func TestAnOverduePromiseIsTheGoalOnceAndTheNextOneIsARisk(t *testing.T) {
	in := fullInput()
	in.Commitments = append(in.Commitments, ClaimIn{
		PersonName: "Ana Roth", Kind: kindCommitmentOurs, Body: "share the reference call",
		Status: statusOpen, SourceID: activityID, DueAt: ptr(at(9)),
	})
	sections := Deterministic(in)
	goal := sectionOf(t, sections, crmcontracts.MeetingBriefSectionKindGoal)
	if len(goal.Sentences) != 1 || !strings.Contains(goal.Sentences[0].Text, "overdue since 8 Aug: send the security pack") {
		t.Fatalf("goal = %+v, want the older overdue promise, dated", goal.Sentences)
	}
	risks := sectionOf(t, sections, crmcontracts.MeetingBriefSectionKindRisks)
	if len(risks.Sentences) != 1 || !strings.Contains(risks.Sentences[0].Text, "share the reference call") {
		t.Fatalf("risks = %+v, want only the promise the goal did not take", risks.Sentences)
	}
	if risks.Sentences[0].Nature != natureAssessment {
		t.Errorf("a risk read out of a record is nature %q, want %q", risks.Sentences[0].Nature, natureAssessment)
	}
	for _, section := range sections {
		// The ledger lists every promise by design; no READING repeats the goal.
		if section.Kind == crmcontracts.MeetingBriefSectionKindCommitments {
			continue
		}
		for _, line := range section.Sentences {
			if section.Kind != crmcontracts.MeetingBriefSectionKindGoal && strings.Contains(line.Text, "send the security pack") {
				t.Errorf("%s repeats the goal's promise: %q", section.Kind, line.Text)
			}
		}
	}
	ledger := sectionOf(t, sections, crmcontracts.MeetingBriefSectionKindCommitments)
	if len(ledger.Sentences) != 2 {
		t.Fatalf("the ledger lists %d promises, want both — it is complete even when the goal names one", len(ledger.Sentences))
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
func TestTheGoalIsTheOpenQuestionWhenNothingOfOursIsOverdue(t *testing.T) {
	in := fullInput()
	in.Commitments[0].DueAt = ptr(at(18))
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

// A delivery meeting is the case the project lines exist for: the engagement
// runs for months after close-won, and the deal that started it is long shut.
func deliveryInput() Input {
	in := fullInput()
	// No open deal, and no promise or question either, so the goal has nothing
	// left to say unless the project speaks.
	in.Deal = nil
	in.Commitments = nil
	in.Project = &ProjectIn{
		ID: projectID, Name: "ERP rollout", Key: "ERP-27",
		Phase: "delivering", TargetEndDate: ptr(at(28)),
	}
	return in
}

func TestADeliveryMeetingWithNoOpenDealStillLeadsWithAGoal(t *testing.T) {
	// The section the package calls the antidote to "the canonical prep
	// failure" used to fall silent exactly here, because its last fallback was
	// the deal's next stage and a delivery meeting has no deal.
	goal := sectionOf(t, Deterministic(deliveryInput()), crmcontracts.MeetingBriefSectionKindGoal)
	if len(goal.Sentences) == 0 {
		t.Fatal("a delivery meeting got no goal; the section exists to prevent exactly that")
	}
	if !strings.Contains(goal.Sentences[0].Text, "ERP rollout") {
		t.Errorf("goal = %q, want it to name the engagement", goal.Sentences[0].Text)
	}
	if goal.Sentences[0].Nature != natureRecommendation {
		t.Error("the goal is an ask, so it must be labelled a recommendation")
	}
}

func TestTheHeaderNamesTheEngagementAndItsKey(t *testing.T) {
	header := sectionOf(t, Deterministic(deliveryInput()), crmcontracts.MeetingBriefSectionKindHeader)
	var found string
	for _, sentence := range header.Sentences {
		if strings.Contains(sentence.Text, "ERP rollout") {
			found = sentence.Text
		}
	}
	if found == "" {
		t.Fatal("the header never names the project; on a two-engagement account that is what says you are in the right room")
	}
	// The key is what a reader sees in subject lines all day, so it rides along.
	if !strings.Contains(found, "ERP-27") {
		t.Errorf("header project line = %q, want the key beside the name", found)
	}
}

func TestAProjectWithNoTargetDateClaimsNoDeadline(t *testing.T) {
	// A deadline nobody set is the invented context the grounding rule forbids.
	in := deliveryInput()
	in.Project.TargetEndDate = nil
	goal := sectionOf(t, Deterministic(in), crmcontracts.MeetingBriefSectionKindGoal)
	if strings.Contains(goal.Sentences[0].Text, "target") {
		t.Errorf("goal = %q, want no target named when the record carries none", goal.Sentences[0].Text)
	}
}

func TestAMeetingWithNoProjectSaysNothingAboutOne(t *testing.T) {
	// Most meetings belong to no project, and a brief that mentioned one
	// anyway would be describing a body of work nobody filed it under.
	// Counted, not string-matched. An assertion that only rejects the fixture's
	// own project name would pass against an unconditional zero-value line —
	// a bare "." in the header — or a project fabricated under another name.
	unprojected := sectionOf(t, Deterministic(fullInput()), crmcontracts.MeetingBriefSectionKindHeader)
	projected := sectionOf(t, Deterministic(deliveryInput()), crmcontracts.MeetingBriefSectionKindHeader)
	// The delivery fixture drops the deal and adds a project, so the two
	// headers carry the same number of lines: meeting, one record line, last
	// touch. Any EXTRA line on the unprojected one is a project line it should
	// not have.
	if len(unprojected.Sentences) != len(projected.Sentences) {
		t.Fatalf("header lines: unprojected %d, projected %d — want the same shape",
			len(unprojected.Sentences), len(projected.Sentences))
	}
	for _, sentence := range unprojected.Sentences {
		if strings.Contains(sentence.Text, "ERP") {
			t.Errorf("unprojected meeting header mentions a project: %q", sentence.Text)
		}
	}
}

func TestTheLastSectionSaysWhenThisRoomLastMet(t *testing.T) {
	in := fullInput()
	in.PriorMeetings = []PriorMeetingIn{
		{ID: activityID, Subject: "Kickoff", StartsAt: at(3)},
	}
	section := sectionOf(t, Deterministic(in), crmcontracts.MeetingBriefSectionKindCompanyContext)
	if len(section.Sentences) != 1 {
		t.Fatalf("prior meetings = %d sentences, want 1", len(section.Sentences))
	}
	// Days, not a date: the reader is placing it against today.
	if !strings.Contains(section.Sentences[0].Text, "7 days ago") {
		t.Errorf("line = %q, want the gap in days", section.Sentences[0].Text)
	}
	if !strings.Contains(section.Sentences[0].Text, "Kickoff") {
		t.Errorf("line = %q, want the meeting named — a gap alone says which conversation?", section.Sentences[0].Text)
	}
}

func TestAFirstMeetingWithARoomInventsNoHistory(t *testing.T) {
	// A section with nothing to say is ABSENT, and background invented for a
	// room nobody has met is the filler the brief's first rule forbids.
	in := fullInput()
	in.PriorMeetings = nil
	for _, section := range Deterministic(in) {
		if section.Kind == crmcontracts.MeetingBriefSectionKindCompanyContext &&
			len(section.Sentences) > 0 {
			t.Errorf("a first meeting got history: %q", section.Sentences[0].Text)
		}
	}
}

func TestEveryPriorMeetingCitesTheMeetingItNames(t *testing.T) {
	// The reader's next move is to open the earlier meeting, so the line must
	// carry the record rather than only describing it.
	in := fullInput()
	in.PriorMeetings = []PriorMeetingIn{{ID: activityID, Subject: "Kickoff", StartsAt: at(3)}}
	section := sectionOf(t, Deterministic(in), crmcontracts.MeetingBriefSectionKindCompanyContext)
	cited := section.Sentences[0].Evidence
	if len(cited) != 1 || cited[0].EntityID != activityID || cited[0].EntityType != citeActivity {
		t.Errorf("evidence = %+v, want the earlier meeting's own activity", cited)
	}
}
