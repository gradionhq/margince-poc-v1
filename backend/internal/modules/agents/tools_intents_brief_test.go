// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// Which answer prep_for_meeting gives, and what it does with the brief when it
// has one.
//
// The tool used to compose its own picture for every anchor, beside a written
// brief only a person could read. The routing below is what ended that, so it
// is what has to stay proved: a meeting gets the brief, everything else gets
// what it always got, and a failure is never dressed up as an answer.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// briefStub records what the tool asked for and answers what the test wants.
type briefStub struct {
	askedFor ids.UUID
	answer   MeetingBriefResult
	err      error
}

func (b *briefStub) read(_ context.Context, activityID ids.UUID) (MeetingBriefResult, error) {
	b.askedFor = activityID
	return b.answer, b.err
}

func aBrief(activity ids.UUID, cited ...MeetingBriefCite) MeetingBriefResult {
	return MeetingBriefResult{
		ActivityID: activity, GeneratedBy: "deterministic",
		Sections: []MeetingBriefPart{{
			Kind:      "goal",
			Sentences: []MeetingBriefLine{{Text: "Close out the security pack.", Evidence: cited}},
		}},
	}
}

func callPrep(t *testing.T, tool prepForMeeting, recordType string, id ids.UUID) (PrepForMeetingResult, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"record_type": recordType, "record_id": id})
	if err != nil {
		t.Fatalf("encoding the arguments: %v", err)
	}
	raw, err := tool.Handle(context.Background(), args)
	if err != nil {
		return PrepForMeetingResult{}, err
	}
	var out PrepForMeetingResult
	if decodeErr := json.Unmarshal(raw, &out); decodeErr != nil {
		t.Fatalf("decoding the answer: %v", decodeErr)
	}
	return out, nil
}

func TestAMeetingAnchorIsAnsweredWithTheWrittenBrief(t *testing.T) {
	meeting := ids.NewV7()
	stub := &briefStub{answer: aBrief(meeting)}
	tool := prepForMeeting{retriever: inertRetriever{}, brief: stub.read}

	got, err := callPrep(t, tool, string(datasource.EntityActivity), meeting)
	if err != nil {
		t.Fatalf("prep_for_meeting: %v", err)
	}
	if got.Brief == nil {
		t.Fatal("a meeting anchor got no brief; the agent is back on the other answer")
	}
	if stub.askedFor != meeting {
		t.Errorf("the tool briefed %s, want the anchor %s", stub.askedFor, meeting)
	}
}

func TestARecordThatIsNotAMeetingIsNeverBriefed(t *testing.T) {
	// A person names a record, not a room. Asking for a brief would be a read
	// the caller did not request and an answer there is no meeting for.
	stub := &briefStub{answer: aBrief(ids.NewV7())}
	tool := prepForMeeting{retriever: inertRetriever{}, brief: stub.read}

	got, err := callPrep(t, tool, string(datasource.EntityPerson), ids.NewV7())
	if err != nil {
		t.Fatalf("prep_for_meeting: %v", err)
	}
	if got.Brief != nil {
		t.Error("a person anchor was briefed")
	}
	if !stub.askedFor.IsZero() {
		t.Errorf("the brief was read for a person anchor (%s); it should never have been asked", stub.askedFor)
	}
}

func TestAnUnwiredBriefSeamStillAnswers(t *testing.T) {
	// A role composed without the seam answers what this tool always answered,
	// rather than losing a read it can still perform.
	tool := prepForMeeting{retriever: inertRetriever{}, brief: nil}

	got, err := callPrep(t, tool, string(datasource.EntityActivity), ids.NewV7())
	if err != nil {
		t.Fatalf("prep_for_meeting with no brief seam: %v", err)
	}
	if got.Brief != nil {
		t.Error("a nil seam produced a brief out of nowhere")
	}
}

func TestAMeetingTheCallerMayNotReadFallsBackRatherThanFailing(t *testing.T) {
	// Not-found is what the service answers for an activity that is not a
	// booked meeting AND for one outside this caller's scope. Both mean "no
	// brief for you here", and the picture they CAN have still stands.
	stub := &briefStub{err: apperrors.ErrNotFound}
	tool := prepForMeeting{retriever: inertRetriever{}, brief: stub.read}

	got, err := callPrep(t, tool, string(datasource.EntityActivity), ids.NewV7())
	if err != nil {
		t.Fatalf("a not-found brief failed the whole call: %v", err)
	}
	if got.Brief != nil {
		t.Error("a refused brief was answered anyway")
	}
}

func TestARealFailureIsNeverDressedUpAsAnAnswer(t *testing.T) {
	// The one that matters. A database fault or a permission failure reported
	// as a brief-less answer looks exactly like an ordinary meeting-less
	// record, and the caller acts on a picture nobody told it was partial.
	stub := &briefStub{err: errors.New("the database went away")}
	tool := prepForMeeting{retriever: inertRetriever{}, brief: stub.read}

	if _, err := callPrep(t, tool, string(datasource.EntityActivity), ids.NewV7()); err == nil {
		t.Fatal("a failed brief read answered successfully with no brief; the caller cannot tell " +
			"that from a meeting that simply has none")
	}
}

func TestEveryRecordTheBriefNamesIsChargedToTheReadBound(t *testing.T) {
	// Naming a record to an agent is handing that record over, which is why
	// noteEvidence charges rather than only recording. The brief names people
	// and conversations the walk beside it never touched, so left uncharged
	// the richest read on this surface would be its cheapest.
	meeting, contact, thread := ids.NewV7(), ids.NewV7(), ids.NewV7()
	stub := &briefStub{answer: aBrief(meeting,
		MeetingBriefCite{RecordType: "person", RecordID: contact},
		MeetingBriefCite{RecordType: "activity", RecordID: thread},
	)}
	tool := prepForMeeting{retriever: inertRetriever{}, brief: stub.read}

	ctx, facts := withEnvelopeFacts(context.Background())
	args, err := json.Marshal(map[string]any{
		"record_type": string(datasource.EntityActivity), "record_id": meeting,
	})
	if err != nil {
		t.Fatalf("encoding the arguments: %v", err)
	}
	if _, err := tool.Handle(ctx, args); err != nil {
		t.Fatalf("prep_for_meeting: %v", err)
	}

	charged := map[ids.UUID]bool{}
	for _, ref := range facts.evidence {
		charged[ref.RecordID] = true
	}
	for name, id := range map[string]ids.UUID{"the meeting": meeting, "the contact": contact, "the thread": thread} {
		if !charged[id] {
			t.Errorf("%s is named in the brief but absent from the envelope, so it was served free", name)
		}
	}
}
