// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package connector

// The further parties to a captured message — everyone in the interaction who
// is neither the mailbox owner nor the counterparty (ACT-DDL-3 / ADR-0078).
//
// They sit beside NormalizedRecord rather than inside connector.go because the
// two ends of an exchange and everyone else are different kinds of fact.
// Direction is defined against the two ends, and a connector reports those
// through Counterparty and its own connection; the parties here are present
// without being either end.

// MessageParticipant is one further party to a captured message.
//
// Email is the raw header address; capture lowercases it, because that is how
// person_email stores one and a case difference would otherwise read as a
// different human. Role is one of the ParticipantRole constants below and is
// closed at the database, so an unknown value is refused rather than stored.
type MessageParticipant struct {
	Email string
	Role  string
}

// The roles a further participant may hold. The set is closed by the
// activity_participant CHECK, so an unlisted value is a constraint violation
// rather than a stored surprise.
//
// These name a HEADER POSITION, not a direction. The third name on a To line
// is a recipient whether the mailbox owner sent the message or received it —
// only the two ends of the exchange are assigned by direction, and capture
// does that from Counterparty rather than from anything a connector reports.
const (
	ParticipantRoleTo        = "to"
	ParticipantRoleCC        = "cc"
	ParticipantRoleAttendee  = "attendee"
	ParticipantRoleOrganizer = "organizer"
)
