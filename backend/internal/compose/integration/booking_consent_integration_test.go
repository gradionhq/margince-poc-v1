// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The authenticated booking's consent passthrough (features/07 §5c): a
// CaptureConsent block on POST /v1/bookings records the subject's grant
// with its proof BEFORE the slot commits, on the same seam the anonymous
// page rides. Without the block the booking stays a plain meeting; an
// unrecordable consent (unknown purpose, missing policy version, no
// linked person) refuses BEFORE any write, leaving the slot free.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// bookingAt shapes one /v1/bookings body for a fixed Tuesday slot,
// linked to the person; extra fields merge over the base.
func bookingAt(start time.Time, personID string, extra anyMap) anyMap {
	body := anyMap{
		"start":   start,
		"end":     start.Add(30 * time.Minute),
		"subject": "Consented discovery call",
		"links":   []anyMap{{"entity_type": "person", "entity_id": personID}},
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestBookingConsentPassthrough(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	purposeID := seededTransactionalPurposeID(t, e)

	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{"full_name": "Carla Consenting"}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person → %d", status)
	}

	// A fixed Tuesday keeps every slot inside business hours.
	tuesday := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	consent := anyMap{
		"purpose_id":     purposeID,
		"policy_version": "pp-2026-01",
		"wording":        "You agree we may contact you about this meeting.",
	}

	assertConsentRefusalsLeaveNothingBehind(t, e, person.ID, tuesday, purposeID)
	assertConsentlessBookingStaysPlain(t, e, person.ID, tuesday)
	assertConsentedBookingLandsGrantWithProof(t, e, person.ID, tuesday, purposeID, consent)
}

// assertConsentRefusalsLeaveNothingBehind drives every unrecordable
// consent shape and proves each refused before a single write: no
// meeting, no consent state, and the slot still free.
func assertConsentRefusalsLeaveNothingBehind(t *testing.T, e *env, personID string, tuesday time.Time, purposeID string) {
	t.Helper()
	slot := tuesday // 10:00 — reused by every refusal, then proven free

	// An unknown purpose cannot be recorded.
	unknownPurpose := bookingAt(slot, personID, anyMap{
		"consent": anyMap{"purpose_id": ids.NewV7().String(), "policy_version": "pp-2026-01"},
	})
	if status := e.call(t, "POST", "/v1/bookings", unknownPurpose, nil, nil); status != 422 {
		t.Fatalf("booking with an unknown consent purpose → %d, want 422", status)
	}

	// The wording version shown to the subject is the proof's anchor.
	noPolicy := bookingAt(slot, personID, anyMap{
		"consent": anyMap{"purpose_id": purposeID},
	})
	if status := e.call(t, "POST", "/v1/bookings", noPolicy, nil, nil); status != 422 {
		t.Fatalf("booking consent without policy_version → %d, want 422", status)
	}

	// Consent is person-keyed: no linked person, nothing to attach to.
	subjectless := bookingAt(slot, personID, anyMap{
		"links":   []anyMap{},
		"consent": anyMap{"purpose_id": purposeID, "policy_version": "pp-2026-01"},
	})
	if status := e.call(t, "POST", "/v1/bookings", subjectless, nil, nil); status != 422 {
		t.Fatalf("booking consent without a linked person → %d, want 422", status)
	}

	var meetings, consents int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM activity WHERE kind = 'meeting'),
		       (SELECT count(*) FROM person_consent)`).Scan(&meetings, &consents); err != nil {
		t.Fatal(err)
	}
	if meetings != 0 || consents != 0 {
		t.Fatalf("refused consents left %d meetings and %d consent rows, want none", meetings, consents)
	}

	// The refused slot was never taken: booking it now succeeds.
	if status := e.call(t, "POST", "/v1/bookings", bookingAt(slot, personID, nil), nil, nil); status != http.StatusCreated {
		t.Fatalf("re-booking the slot a refused consent named → %d, want 201 (the refusal must not consume it)", status)
	}
}

// assertConsentlessBookingStaysPlain books without a consent block and
// proves the meeting lands with zero consent side effects.
func assertConsentlessBookingStaysPlain(t *testing.T, e *env, personID string, tuesday time.Time) {
	t.Helper()
	var booked struct {
		Kind string `json:"kind"`
	}
	if status := e.call(t, "POST", "/v1/bookings", bookingAt(tuesday.Add(time.Hour), personID, nil), nil, &booked); status != http.StatusCreated || booked.Kind != "meeting" {
		t.Fatalf("consentless booking → %d %+v, want a plain 201 meeting", status, booked)
	}
	var consents int
	if err := e.owner.QueryRow(context.Background(), `SELECT count(*) FROM person_consent`).Scan(&consents); err != nil {
		t.Fatal(err)
	}
	if consents != 0 {
		t.Fatalf("a consentless booking minted %d consent rows, want 0", consents)
	}
}

// assertConsentedBookingLandsGrantWithProof books with the consent block
// and proves both facts of the seam: the meeting on the timeline AND the
// person_consent grant carrying the passthrough proof verbatim.
func assertConsentedBookingLandsGrantWithProof(t *testing.T, e *env, personID string, tuesday time.Time, purposeID string, consent anyMap) {
	t.Helper()
	if status := e.call(t, "POST", "/v1/bookings",
		bookingAt(tuesday.Add(2*time.Hour), personID, anyMap{"consent": consent}), nil, nil); status != http.StatusCreated {
		t.Fatalf("consented booking → %d", status)
	}

	var state string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT state FROM person_consent WHERE person_id = $1 AND purpose_id = $2`,
		personID, purposeID).Scan(&state); err != nil {
		t.Fatalf("reading the consent state the booking recorded: %v", err)
	}
	if state != "granted" {
		t.Fatalf("consent state = %q, want granted", state)
	}
	var policyVersion, policyText string
	if err := e.owner.QueryRow(context.Background(), `
		SELECT policy_version, policy_text FROM consent_event
		WHERE person_id = $1 AND purpose_id = $2`,
		personID, purposeID).Scan(&policyVersion, &policyText); err != nil {
		t.Fatalf("reading the proof event: %v", err)
	}
	if policyVersion != "pp-2026-01" || policyText != "You agree we may contact you about this meeting." {
		t.Fatalf("proof event lost the passthrough: version=%q text=%q", policyVersion, policyText)
	}
}
