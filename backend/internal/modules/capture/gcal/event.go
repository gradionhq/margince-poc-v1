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
// handle. The owner's own domain gives the internal floor; the workspace's
// registered domains (CAP-DDL-1) are the authority and are applied by the
// writer, over the full party set this reports.
type meeting struct {
	id           string
	subject      string
	body         string
	occurredAt   time.Time
	cancelled    bool
	organizerDom string
	hasExternal  bool // any party outside the OWNER's own domain — the floor, see SkipReason
	// addresses is every party the event names — organizer and attendees,
	// the owner included. The internal-vs-external decision is taken over this
	// set by the capture writer against the workspace's registered domains
	// (ADR-0082/A127); this package reports the parties and judges none.
	addresses []string
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
		// The organizer counts as a party: an externally-organized meeting is a
		// customer touch even when the owner is the only listed attendee.
		hasExternal:  external > 0 || (organizerDom != "" && organizerDom != ownerDom),
		addresses:    eventAddresses(ev, owner),
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

	return connector.CapParticipants(out)
}

// SkipReason names why a meeting is intentionally dropped, or reports that it
// should be captured: a cancelled event and one with no stable id are dropped
// (nothing to key on / nothing to log).
//
// The owner's domain is a FLOOR here, not the authority. The workspace's
// registered domains decide internal-vs-external for mail and calendar alike
// (formulas §20, ADR-0082/A127), and only the capture writer can read them —
// a connector holds no database handle by design. This drops what the owner's
// own domain alone proves internal; the writer widens that, never narrows it.
func (m meeting) SkipReason() (string, bool) {
	if m.id == "" {
		return "no event id", true
	}
	if m.cancelled {
		return "cancelled", true
	}
	// An event naming nobody but the owner is a block in their own calendar —
	// focus time, a reminder, a flight. Nobody was met, so there is no
	// interaction to log. This asks whether there was a second party at all,
	// not whose side they were on, so it needs no knowledge of any domain.
	if len(m.addresses) <= 1 {
		return "no party besides the owner", true
	}
	// The owner-domain floor. The workspace's registered domains are the
	// authority (formulas §20) and the writer applies them over the full party
	// set, which is wider than this — but that set can be empty or incomplete,
	// and an internal meeting stored while it is would be readable by the whole
	// workspace. This drops what the owner's own domain alone can prove
	// internal; the writer widens it, never narrows it.
	if !m.hasExternal {
		return "no party outside the owner's domain", true
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
		Addresses:    m.addresses,
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

// eventAddresses returns every address the event names — organizer, attendees
// and the connected owner — lowercased, deduplicated, order preserved. The
// owner is included because the internal decision is taken against the
// workspace's registered domains rather than against the owner, and a
// workspace that has not registered its own domain should not have that
// asserted on its behalf here.
func eventAddresses(ev rawEvent, owner string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(address string) {
		address = strings.ToLower(strings.TrimSpace(address))
		if address == "" || seen[address] {
			return
		}
		seen[address] = true
		out = append(out, address)
	}
	add(ev.Organizer.Email)
	for _, a := range ev.Attendees {
		add(a.Email)
	}
	add(owner)
	return out
}
