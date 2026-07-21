// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gcal

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// eventJSON builds a raw Calendar v3 event resource for the mapper fixtures.
// The organizer is explicit so a fixture can model an externally-organized
// meeting the owner merely attends, not only owner-organized ones.
func eventJSON(t *testing.T, id, status, summary, start, organizer string, attendees ...string) []byte {
	t.Helper()
	att := make([]map[string]string, 0, len(attendees))
	for _, a := range attendees {
		att = append(att, map[string]string{"email": a})
	}
	b, err := json.Marshal(map[string]any{
		"id":        id,
		"status":    status,
		"summary":   summary,
		"start":     map[string]string{"dateTime": start},
		"organizer": map[string]string{"email": organizer},
		"attendees": att,
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return b
}

func TestParseEventMapsMeetingActivity(t *testing.T) {
	raw := eventJSON(t, "evt-1", "confirmed", "Kickoff", "2026-07-16T10:00:00Z",
		gcalOwner, gcalOwner, "client@acme.com")
	m, err := parseEvent(raw, gcalOwner)
	if err != nil {
		t.Fatalf("parseEvent: %v", err)
	}
	if reason, skip := m.SkipReason(); skip {
		t.Fatalf("a meeting with an external attendee must not skip, got %q", reason)
	}
	rec := m.ToRecord("gcal", raw)
	if rec.EntityType != datasource.EntityActivity {
		t.Errorf("EntityType = %v, want activity", rec.EntityType)
	}
	if rec.NaturalKey.SourceSystem != "gcal" || rec.NaturalKey.SourceID != "evt-1" {
		t.Errorf("NaturalKey = %+v, want gcal/evt-1", rec.NaturalKey)
	}
	if rec.Source != "gcal:evt-1" || rec.CapturedBy != "connector:gcal" {
		t.Errorf("provenance = (%q,%q), want (gcal:evt-1, connector:gcal)", rec.Source, rec.CapturedBy)
	}
	fields, ok := rec.Fields.(capture.ActivityFields)
	if !ok {
		t.Fatalf("Fields is %T, want capture.ActivityFields", rec.Fields)
	}
	if fields.Kind != "meeting" {
		t.Errorf("Kind = %q, want meeting", fields.Kind)
	}
	if fields.Subject != "Kickoff" {
		t.Errorf("Subject = %q, want Kickoff", fields.Subject)
	}
	if fields.Direction != "" {
		t.Errorf("Direction = %q, want empty (a meeting is not directional)", fields.Direction)
	}
	want := time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)
	if !fields.OccurredAt.Equal(want) {
		t.Errorf("OccurredAt = %v, want %v", fields.OccurredAt, want)
	}
}

func TestToRecordCarriesExclusionDomainsForTheGate(t *testing.T) {
	raw := eventJSON(t, "evt-2", "confirmed", "Sync", "2026-07-16T10:00:00Z",
		gcalOwner, "client@acme.com", "vendor@beta.io")
	rec := mustRecord(t, raw)
	if rec.Match.SenderDomain != "myco.com" {
		t.Errorf("Match.SenderDomain = %q, want myco.com (the organizer)", rec.Match.SenderDomain)
	}
	// De-duped attendee domains feed the RC-2 personal-mail gate in the Sink.
	if len(rec.Match.RecipientDomains) != 2 {
		t.Errorf("Match.RecipientDomains = %v, want [acme.com beta.io]", rec.Match.RecipientDomains)
	}
}

func TestSkipReasonAllInternalYieldsZeroRows(t *testing.T) {
	// Owner + two colleagues on the same domain: an internal meeting → skip.
	raw := eventJSON(t, "evt-3", "confirmed", "Standup", "2026-07-16T09:00:00Z",
		gcalOwner, gcalOwner, "peer@myco.com", "boss@myco.com")
	m := mustParse(t, raw)
	reason, skip := m.SkipReason()
	if !skip || reason != "all-internal attendees" {
		t.Fatalf("all-internal meeting: got (%q, skip=%v), want all-internal skip", reason, skip)
	}
}

func TestSkipReasonSoloEventSkips(t *testing.T) {
	// A personal block with no attendees is not a captured touch.
	raw := eventJSON(t, "evt-4", "confirmed", "Focus time", "2026-07-16T09:00:00Z", gcalOwner)
	if _, skip := mustParse(t, raw).SkipReason(); !skip {
		t.Fatal("a solo event (no attendees) must skip")
	}
}

func TestSkipReasonCancelledSkips(t *testing.T) {
	raw := eventJSON(t, "evt-5", "cancelled", "Cancelled call", "2026-07-16T09:00:00Z",
		gcalOwner, "client@acme.com")
	reason, skip := mustParse(t, raw).SkipReason()
	if !skip || reason != "cancelled" {
		t.Fatalf("cancelled event: got (%q, skip=%v), want cancelled skip", reason, skip)
	}
}

func TestExternallyOrganizedMeetingIsCaptured(t *testing.T) {
	// A client organizes; the owner attends. There is an external party, so it
	// is a real customer touch → captured, with the organizer's domain leading
	// the RC-2 match attributes.
	raw := eventJSON(t, "evt-ext-org", "confirmed", "Vendor review", "2026-07-16T14:00:00Z",
		"host@acme.com", gcalOwner, "host@acme.com")
	m := mustParse(t, raw)
	if _, skip := m.SkipReason(); skip {
		t.Fatal("an externally-organized meeting the owner attends must be captured")
	}
	if got := m.ToRecord("gcal", raw).Match.SenderDomain; got != "acme.com" {
		t.Errorf("Match.SenderDomain = %q, want acme.com (the external organizer)", got)
	}
}

func TestExternalOrganizerWithOnlyOwnerAttendeeIsCaptured(t *testing.T) {
	// An external party organizes and only the owner is listed as an attendee.
	// The external organizer alone makes it a customer touch → captured, not
	// dropped as all-internal.
	raw := eventJSON(t, "evt-org-only", "confirmed", "Client-hosted call", "2026-07-16T15:00:00Z",
		"host@acme.com", gcalOwner)
	if reason, skip := mustParse(t, raw).SkipReason(); skip {
		t.Fatalf("external organizer with only the owner attending must be captured, got skip %q", reason)
	}
}

func TestSkipReasonMixedInternalExternalKeeps(t *testing.T) {
	// One external attendee among colleagues is a customer touch → keep.
	raw := eventJSON(t, "evt-6", "confirmed", "Demo", "2026-07-16T11:00:00Z",
		gcalOwner, gcalOwner, "peer@myco.com", "client@acme.com")
	if _, skip := mustParse(t, raw).SkipReason(); skip {
		t.Fatal("a meeting with at least one external attendee must be captured")
	}
}

func TestParseEventAllDayFallsBackToDate(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"id": "evt-7", "status": "confirmed", "summary": "Onsite",
		"start":     map[string]string{"date": "2026-07-16"},
		"organizer": map[string]string{"email": gcalOwner},
		"attendees": []map[string]string{{"email": "client@acme.com"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m := mustParse(t, raw)
	// A timezone-free all-day date is anchored at noon UTC so it keeps its
	// calendar date across the ±12h of real-world offsets (midnight UTC would
	// slip to the previous day for any zone west of UTC).
	want := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	if !m.occurredAt.Equal(want) {
		t.Errorf("all-day OccurredAt = %v, want %v (noon UTC)", m.occurredAt, want)
	}
}

func TestParseEventBodyFoldsParticipants(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"id": "evt-8", "status": "confirmed", "summary": "Review",
		"description": "Quarterly review agenda",
		"start":       map[string]string{"dateTime": "2026-07-16T10:00:00Z"},
		"organizer":   map[string]string{"email": gcalOwner},
		"attendees":   []map[string]string{{"email": "client@acme.com"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := mustRecord(t, raw)
	fields, ok := rec.Fields.(capture.ActivityFields)
	if !ok {
		t.Fatalf("Fields is %T, want capture.ActivityFields", rec.Fields)
	}
	body := fields.Body
	for _, want := range []string{"Organizer: " + gcalOwner, "Attendees: client@acme.com", "Quarterly review agenda"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestBodyIsTruncatedToBudget(t *testing.T) {
	longDesc := strings.Repeat("a", maxBodyLen+500)
	raw, err := json.Marshal(map[string]any{
		"id": "evt-long", "status": "confirmed", "summary": "Big",
		"description": longDesc,
		"start":       map[string]string{"dateTime": "2026-07-16T10:00:00Z"},
		"organizer":   map[string]string{"email": gcalOwner},
		"attendees":   []map[string]string{{"email": "client@acme.com"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := mustParse(t, raw).body
	if len([]rune(body)) > maxBodyLen+1 { // +1 for the ellipsis rune
		t.Errorf("body of %d runes exceeds the cap %d", len([]rune(body)), maxBodyLen)
	}
	if !strings.HasSuffix(body, "…") {
		t.Error("a truncated body must end with an ellipsis")
	}
}

func TestAttendeeWithoutDomainCountsAsExternal(t *testing.T) {
	// A malformed attendee address has no parseable domain; unknown ≠ internal,
	// so the meeting is captured rather than silently dropped.
	raw := eventJSON(t, "evt-x", "confirmed", "Odd", "2026-07-16T10:00:00Z",
		gcalOwner, gcalOwner, "weird-address")
	if _, skip := mustParse(t, raw).SkipReason(); skip {
		t.Fatal("an attendee with no parseable domain must count as external (keep the meeting)")
	}
}

func TestParseEventRejectsMalformedJSON(t *testing.T) {
	if _, err := parseEvent([]byte("}not json{"), gcalOwner); err == nil {
		t.Fatal("parseEvent must reject malformed event bytes")
	}
}

// --- helpers -------------------------------------------------------------

const gcalOwner = "rep@myco.com"

func mustParse(t *testing.T, raw []byte) meeting {
	t.Helper()
	m, err := parseEvent(raw, gcalOwner)
	if err != nil {
		t.Fatalf("parseEvent: %v", err)
	}
	return m
}

func mustRecord(t *testing.T, raw []byte) connector.NormalizedRecord {
	t.Helper()
	return mustParse(t, raw).ToRecord("gcal", raw)
}
