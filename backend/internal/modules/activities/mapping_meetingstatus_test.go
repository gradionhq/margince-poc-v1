// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// meeting_status pairs with kind meeting and nothing else. The forward case
// matters because the lead status ladder reads booked/held as engagement —
// a mapping that dropped the field would leave a hand-logged meeting unable
// to move a lead while a synced one does. The refusal matters because the
// database CHECK constrains only the vocabulary, not the pairing: without it
// a note carrying `held` stores silently as a meeting-shaped fact.

import (
	"errors"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestLogActivityInputCarriesAMeetingsStatus(t *testing.T) {
	status := crmcontracts.CreateActivityRequestMeetingStatusHeld

	in, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind:          crmcontracts.CreateActivityRequestKindMeeting,
		MeetingStatus: &status,
		Source:        "human",
	})
	if err != nil {
		t.Fatalf("mapping a held meeting: %v", err)
	}
	if in.MeetingStatus == nil || *in.MeetingStatus != "held" {
		t.Fatalf("MeetingStatus = %v, want held — dropped, a hand-logged meeting cannot earn a lead's engaged step", in.MeetingStatus)
	}
}

func TestLogActivityInputRefusesAMeetingStatusOnANote(t *testing.T) {
	status := crmcontracts.CreateActivityRequestMeetingStatusHeld

	_, err := LogActivityInputFrom(crmcontracts.CreateActivityRequest{
		Kind:          crmcontracts.CreateActivityRequestKindNote,
		MeetingStatus: &status,
		Source:        "human",
	})

	var fault *MeetingStatusKindError
	if !errors.As(err, &fault) {
		t.Fatalf("err = %v, want a MeetingStatusKindError — a note carrying held must be a 422 naming the field", err)
	}
	if field, code, _ := fault.FieldFault(); field != "meeting_status" || code != faultNotValidForKind {
		t.Fatalf("FieldFault = (%q, %q), want (meeting_status, %s)", field, code, faultNotValidForKind)
	}
}
