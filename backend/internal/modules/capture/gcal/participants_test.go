// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gcal

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// A meeting's parties were folded into body text because there was nowhere
// structured to put them. These are the same people in a form the interaction
// graph can read.
func TestParticipantsOfNamesTheOrganizerAndAttendees(t *testing.T) {
	raw := []byte(`{
	  "id": "evt-1",
	  "status": "confirmed",
	  "summary": "Pricing review",
	  "start": {"dateTime": "2026-06-04T09:00:00Z"},
	  "organizer": {"email": "Bob@Target.com"},
	  "attendees": [
	    {"email": "me@myco.com"},
	    {"email": "sam@target.com"},
	    {"email": "bob@target.com"}
	  ]
	}`)
	parties, err := ParticipantsOf(raw, "me@myco.com")
	if err != nil {
		t.Fatalf("ParticipantsOf: %v", err)
	}
	roles := map[string]string{}
	for _, p := range parties {
		if _, dup := roles[p.Email]; dup {
			t.Errorf("%s appears twice; one party is one row", p.Email)
		}
		roles[p.Email] = p.Role
	}
	if _, found := roles["me@myco.com"]; found {
		t.Error("the mailbox owner was recorded from the header; their own row carries the user id the graph joins on")
	}
	// The organizer's address is capitalized in the payload and lowercase in
	// the attendee list. Those are one human, and person_email stores an
	// address lowercased — a case difference here would read as two people.
	if roles["bob@target.com"] != connector.ParticipantRoleOrganizer {
		t.Errorf("organizer role = %q, want %q — organizing outranks attending, and the address folds case",
			roles["bob@target.com"], connector.ParticipantRoleOrganizer)
	}
	if roles["sam@target.com"] != connector.ParticipantRoleAttendee {
		t.Errorf("attendee role = %q, want %q", roles["sam@target.com"], connector.ParticipantRoleAttendee)
	}
}

// A payload this parser cannot decompose must be reported, not silently
// treated as a meeting nobody attended.
func TestParticipantsOfRefusesAnUnreadablePayload(t *testing.T) {
	if _, err := ParticipantsOf([]byte("not json"), "me@myco.com"); err == nil {
		t.Error("an unparseable event returned no error, so the replay would record it as having no parties")
	}
}
