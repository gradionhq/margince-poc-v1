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
	attendeeDoms []string // lowercased, de-duped attendee domains — for the RC-2 gate
	hasExternal  bool     // any party (organizer or attendee) outside the owner's domain
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
	attendeeDoms, attendeeEmails, external := classifyAttendees(ev.Attendees, ownerDom)
	organizerDom := domainOf(ev.Organizer.Email)

	return meeting{
		id:           strings.TrimSpace(ev.ID),
		subject:      strings.TrimSpace(ev.Summary),
		body:         buildBody(ev, attendeeEmails),
		occurredAt:   parseStart(ev.Start),
		cancelled:    strings.EqualFold(strings.TrimSpace(ev.Status), "cancelled"),
		organizerDom: organizerDom,
		attendeeDoms: attendeeDoms,
		// An externally-organized meeting is a customer touch even if the owner
		// is the only listed attendee — fold the organizer into the signal.
		hasExternal: external > 0 || isExternalDomain(organizerDom, ownerDom),
	}, nil
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
		Source:     connectorName + ":" + m.id,
		CapturedBy: "connector:" + connectorName,
		Raw:        raw,
		Match: connector.ExclusionAttrs{
			SenderDomain:     m.organizerDom,
			RecipientDomains: m.attendeeDoms,
		},
	}
}

// classifyAttendees returns the de-duped attendee domains (for the RC-2 gate),
// the attendee emails (for the body header, order-preserving), and the count of
// attendees whose domain differs from the owner's — the external signal behind
// the all-internal skip. An attendee with no parseable domain is treated as
// external (unknown ≠ internal): capturing a possibly-external touch beats
// silently dropping it.
func classifyAttendees(attendees []eventActor, ownerDom string) (domains, emails []string, external int) {
	seen := map[string]struct{}{}
	for _, a := range attendees {
		email := strings.TrimSpace(a.Email)
		if email == "" {
			continue
		}
		emails = append(emails, email)
		dom := domainOf(email)
		if dom == "" || ownerDom == "" || dom != ownerDom {
			external++
		}
		if dom == "" {
			continue
		}
		if _, dup := seen[dom]; dup {
			continue
		}
		seen[dom] = struct{}{}
		domains = append(domains, dom)
	}
	return domains, emails, external
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
