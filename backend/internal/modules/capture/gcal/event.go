// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// This file is the pure Google Calendar event → meeting-activity mapping:
// no provider handle, no I/O beyond reading the in-memory event bytes. It is
// the calendar analogue of capture/mailmap — the test-guarded surface a
// connector's Sync and Normalize compose, so the classification (all-internal
// skip, cancelled skip) and the field mapping are proven by fixtures, not a
// live calendar. It is kept in the gcal package (not a shared subpackage)
// because gcal is the only calendar connector today (ADR-0054 §3: flat by
// default; grow a subpackage only when a second concrete caller appears).

package gcal

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// maxBodyLen caps the stored meeting body — the timeline needs a legible
// summary of who/what, not a multi-kilobyte agenda paste.
const maxBodyLen = 8000

// rawEvent is the subset of a Google Calendar v3 event resource this mapping
// reads. Unknown fields are ignored — the raw original is stored verbatim as
// evidence (memory-first), so nothing is lost by mapping only what we use.
type rawEvent struct {
	ID          string        `json:"id"`
	Status      string        `json:"status"` // "confirmed" | "tentative" | "cancelled"
	Summary     string        `json:"summary"`
	Description string        `json:"description"`
	Start       eventDateTime `json:"start"`
	Organizer   eventActor    `json:"organizer"`
	Attendees   []eventActor  `json:"attendees"`
}

// eventDateTime is a calendar timestamp: dateTime (RFC3339) for a timed event,
// or date (YYYY-MM-DD) for an all-day one.
type eventDateTime struct {
	DateTime string `json:"dateTime"` //nolint:tagliatelle // Google's wire format (camelCase); must match to decode
	Date     string `json:"date"`
}

// eventActor is one organizer/attendee: the email is all this mapping needs to
// resolve the counterparty and the internal-vs-external classification.
type eventActor struct {
	Email string `json:"email"`
}

// meeting is the pure, classified result of reading one calendar event against
// the connected mailbox owner — everything the mapping needs, with no provider
// handle. The owner's own email domain is the internal-vs-external signal
// (formulas §20, owner-domain subset: the multi-domain workspace_email_domain
// registry, CAP-DDL-1, is a separate slice).
type meeting struct {
	id           string
	subject      string
	body         string
	occurredAt   time.Time
	cancelled    bool
	organizerDom string
	hasExternal  bool // any party (organizer or attendee) outside the owner's domain
	// participants are the organizer and attendees as structured rows. The body
	// header still spells them out for the timeline; these are the same people
	// in a form the interaction graph can actually read.
	participants []connector.MessageParticipant
}

// parseEvent reads one raw Calendar event resource and classifies it against
// the mailbox owner (whose domain marks "internal"). It is pure — the bytes are
// already in memory — so the whole mapping is fixture-provable.
func parseEvent(raw []byte, owner string) (meeting, error) {
	var ev rawEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return meeting{}, fmt.Errorf("gcal: parsing calendar event: %w", err)
	}

	ownerDom := domainOf(owner)
	attendeeEmails, external := classifyAttendees(ev.Attendees, ownerDom)
	organizerDom := domainOf(ev.Organizer.Email)

	return meeting{
		id:           strings.TrimSpace(ev.ID),
		subject:      strings.TrimSpace(ev.Summary),
		body:         buildBody(ev, attendeeEmails),
		occurredAt:   parseStart(ev.Start),
		cancelled:    strings.EqualFold(strings.TrimSpace(ev.Status), "cancelled"),
		organizerDom: organizerDom,
		// An externally-organized meeting is a customer touch even if the owner
		// is the only listed attendee — fold the organizer into the signal.
		hasExternal:  external > 0 || isExternalDomain(organizerDom, ownerDom),
		participants: meetingParties(ev, strings.ToLower(strings.TrimSpace(owner))),
	}, nil
}

// ParticipantsOf reads the organizer and attendees out of one stored event
// resource — the calendar twin of mailmap.ParticipantsOf, for the replay pass
// that recovers meetings captured before participants were recorded.
func ParticipantsOf(raw []byte, owner string) ([]connector.MessageParticipant, error) {
	m, err := parseEvent(raw, owner)
	if err != nil {
		return nil, err
	}
	return m.participants, nil
}

// maxParticipants bounds how many parties one meeting may contribute, for the
// same reason mailmap bounds a To line: past a certain size the invitee list
// is a broadcast rather than a meeting, and every name on it would otherwise
// read as somebody this workspace has a relationship with.
const maxParticipants = 50

// meetingParties returns the organizer and attendees as participant rows,
// excluding the mailbox owner.
//
// The owner is excluded because capture stamps them separately from the
// connection that produced the event — that row carries their user_id, which
// is what the interaction graph joins on, whereas a row built from this header
// would carry only an address and join nothing.
//
// Organizer wins over attendee when the same address holds both, which is the
// common case for a meeting somebody scheduled and then attended: organizing
// is the stronger statement about their part in it.
func meetingParties(ev rawEvent, ownerLower string) []connector.MessageParticipant {
	seen := map[string]bool{}
	if ownerLower != "" {
		seen[ownerLower] = true
	}

	var out []connector.MessageParticipant
	add := func(email, role string) {
		address := strings.ToLower(strings.TrimSpace(email))
		if address == "" || seen[address] {
			return
		}
		seen[address] = true
		out = append(out, connector.MessageParticipant{Email: address, Role: role})
	}
	add(ev.Organizer.Email, connector.ParticipantRoleOrganizer)
	for _, a := range ev.Attendees {
		add(a.Email, connector.ParticipantRoleAttendee)
	}

	if len(out) > maxParticipants {
		return nil
	}
	return out
}

// isExternalDomain reports whether dom is a real domain outside the owner's —
// the atom behind both the attendee and organizer external checks. An empty
// domain (unparseable/absent) is not counted as external on its own.
func isExternalDomain(dom, ownerDom string) bool {
	return dom != "" && dom != ownerDom
}

// SkipReason names why a meeting is intentionally dropped, or reports that it
// should be captured. The all-internal rule (formulas §20) is the load-bearing
// one: an event with no attendee outside the owner's own domain is an internal
// meeting and yields zero CRM rows. A cancelled event and one with no stable id
// are dropped too (nothing to key on / nothing to log).
func (m meeting) SkipReason() (string, bool) {
	if m.id == "" {
		return "no event id", true
	}
	if m.cancelled {
		return "cancelled", true
	}
	if !m.hasExternal {
		return "all-internal attendees", true
	}
	return "", false
}

// ID is the Calendar event id — the idempotency source id gcal keys on
// (ACT-DDL-1: capture key is the event id per workspace).
func (m meeting) ID() string { return m.id }

// ToRecord builds the provenance-stamped meeting activity for connectorName
// ("gcal"). The organizer and attendees are folded into a compact header on
// the body — the activity schema has no participant column, and the timeline
// needs to show who the meeting was with (the same shape mailmap uses for
// From/To). Match carries the organizer + attendee domains so the ONE Sink's
// RC-2 personal-mail gate covers calendar exactly as it covers mail.
func (m meeting) ToRecord(connectorName string, raw []byte) connector.NormalizedRecord {
	return connector.NormalizedRecord{
		EntityType: datasource.EntityActivity,
		NaturalKey: connector.NaturalKey{SourceSystem: connectorName, SourceID: m.id},
		Fields: capture.ActivityFields{
			Kind:       "meeting",
			Subject:    m.subject,
			Body:       m.body,
			OccurredAt: m.occurredAt,
			// A meeting is not directional (no inbound/outbound sender).
			Direction: "",
		},
		Source:       connectorName + ":" + m.id,
		CapturedBy:   "connector:" + connectorName,
		Raw:          raw,
		Participants: m.participants,
	}
}

// classifyAttendees returns the de-duped attendee domains (for the RC-2 gate),
// the attendee emails (for the body header, order-preserving), and the count of
// attendees whose domain differs from the owner's — the external signal behind
// the all-internal skip. An attendee with no parseable domain is treated as
// external (unknown ≠ internal): capturing a possibly-external touch beats
// silently dropping it.
func classifyAttendees(attendees []eventActor, ownerDom string) (emails []string, external int) {
	for _, a := range attendees {
		email := strings.TrimSpace(a.Email)
		if email == "" {
			continue
		}
		emails = append(emails, email)
		if dom := domainOf(email); dom == "" || ownerDom == "" || dom != ownerDom {
			external++
		}
	}
	return emails, external
}

// buildBody folds the organizer, the attendee list, and the event description
// into the stored meeting body, bounded to a legible excerpt.
func buildBody(ev rawEvent, attendeeEmails []string) string {
	header := "Organizer: " + orDash(strings.TrimSpace(ev.Organizer.Email))
	if len(attendeeEmails) > 0 {
		header += "\nAttendees: " + strings.Join(attendeeEmails, ", ")
	}
	body := header
	if desc := strings.TrimSpace(ev.Description); desc != "" {
		body = header + "\n\n" + desc
	}
	return truncate(body, maxBodyLen)
}

// parseStart reads the event's start: a timed dateTime (RFC3339) preferred,
// falling back to an all-day date. A start we cannot read yields the zero time
// — the Sink then stamps capture time honestly rather than sorting the row to
// the beginning of history.
func parseStart(start eventDateTime) time.Time {
	if dt := strings.TrimSpace(start.DateTime); dt != "" {
		if t, err := time.Parse(time.RFC3339, dt); err == nil {
			return t.UTC()
		}
	}
	if d := strings.TrimSpace(start.Date); d != "" {
		if t, err := time.Parse("2006-01-02", d); err == nil {
			// An all-day date is calendar-local with no timezone; time.Parse
			// reads it as midnight UTC, which lands on the PREVIOUS day for any
			// zone west of UTC. Anchor at noon UTC so the stored instant keeps
			// the intended calendar date across the whole ±12h range of real
			// offsets, absent a per-event timezone.
			return t.Add(12 * time.Hour).UTC()
		}
	}
	return time.Time{}
}

// domainOf returns the lowercased domain part of an address, or "" if it
// carries no "@". It splits at the LAST "@" so a quoted local part containing
// one still yields the domain.
func domainOf(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if idx := strings.LastIndex(addr, "@"); idx >= 0 {
		return addr[idx+1:]
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	// Back off to a rune boundary so the stored excerpt is never a broken
	// UTF-8 sequence.
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
