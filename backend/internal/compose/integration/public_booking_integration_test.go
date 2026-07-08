// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The anonymous booking surface (feedback/14 — B-EP09.14): no session,
// no workspace header — the slug is the resolver. Consent is mandatory
// and validated before any write; the booker is idempotent on email;
// the response discloses nothing beyond the slot; the exclusion
// constraint answers slot_taken; the surface is throttled.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// publicCall hits the API with NO cookie jar and NO workspace header —
// the anonymous booker's actual shape.
//
//craft:ignore naked-any generic JSON test helper: body and out are each call's own shape
func publicCall(t *testing.T, e *env, method, path string, body any, headers map[string]string, out any) int {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, reqBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer closeBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decoding %q: %v", method, path, raw, err)
		}
	}
	return resp.StatusCode
}

// bookingSlug reads the bootstrap-seeded page slug through the owner
// connection (booking_page is the non-RLS resolver).
func bookingSlug(t *testing.T, e *env) string {
	t.Helper()
	var slug string
	if err := e.owner.QueryRow(context.Background(), `SELECT slug FROM booking_page`).Scan(&slug); err != nil {
		t.Fatalf("reading the seeded booking page: %v", err)
	}
	return slug
}

// nextMonday anchors the scenario inside a future business day.
func nextMonday() time.Time {
	day := time.Now().UTC().AddDate(0, 0, 1)
	for day.Weekday() != time.Monday {
		day = day.AddDate(0, 0, 1)
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, time.UTC)
}

// seededTransactionalPurposeID reads the bootstrap-seeded consent
// catalog as the admin session and resolves the transactional purpose
// the booker will consent to.
func seededTransactionalPurposeID(t *testing.T, e *env) string {
	t.Helper()
	var purposes struct {
		Data []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if status := e.call(t, "GET", "/v1/consent-purposes", nil, nil, &purposes); status != http.StatusOK {
		t.Fatalf("purposes → %d", status)
	}
	var purposeID string
	for _, p := range purposes.Data {
		if p.Key == "transactional" {
			purposeID = p.ID
		}
	}
	if purposeID == "" {
		t.Fatal("no seeded transactional purpose")
	}
	return purposeID
}

// assertAnonymousAvailability checks the no-session availability read:
// 200 with free/busy slots only, and an unknown slug reads as absent.
func assertAnonymousAvailability(t *testing.T, e *env, base, window string) {
	t.Helper()
	// Anonymous availability: 200, free/busy slots only.
	var avail struct {
		Slots []struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"slots"`
	}
	if status := publicCall(t, e, "GET", base+"/availability"+window, nil, nil, &avail); status != http.StatusOK {
		t.Fatalf("anonymous availability → %d", status)
	}
	if len(avail.Slots) == 0 {
		t.Fatal("no free slots on an empty Monday")
	}

	// Unknown slug reads as absent.
	if status := publicCall(t, e, "GET", "/v1/public/booking/no-such-slug/availability"+window, nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("unknown slug → %d, want 404", status)
	}
}

// assertBookingRequiresValidConsent checks consent is validated before
// any write: no consent and a bogus purpose are both 422s that leave
// zero person rows behind.
func assertBookingRequiresValidConsent(t *testing.T, e *env, base string, monday time.Time) {
	t.Helper()
	// A booking without consent is refused before any write.
	noConsent := anyMap{
		"start": monday.Add(1 * time.Hour), "end": monday.Add(90 * time.Minute),
		"booker": anyMap{"name": "Anna Anonymous", "email": "anna@visitor.example"},
	}
	if status := publicCall(t, e, "POST", base, noConsent, nil, nil); status != 422 {
		t.Fatalf("booking without consent → %d, want 422", status)
	}
	// A bogus purpose is refused before any write too.
	badPurpose := anyMap{
		"start": monday.Add(1 * time.Hour), "end": monday.Add(90 * time.Minute),
		"booker":  anyMap{"name": "Anna Anonymous", "email": "anna@visitor.example"},
		"consent": anyMap{"purpose_id": "018f0000-0000-7000-8000-000000000000", "policy_version": "pp-2026-01"},
	}
	if status := publicCall(t, e, "POST", base, badPurpose, nil, nil); status != 422 {
		t.Fatalf("booking with unknown purpose → %d, want 422", status)
	}
	var persons int
	if err := e.owner.QueryRow(context.Background(), `SELECT count(*) FROM person`).Scan(&persons); err != nil {
		t.Fatal(err)
	}
	if persons != 0 {
		t.Fatalf("refused bookings left %d person rows, want 0", persons)
	}
}

// bookHappyPathSlot books the first slot (201 with the slot and NOTHING
// else disclosed) plus a second slot under a case-folded email,
// asserting the booker lands as ONE person. Returns the first booking
// body so the taken slot can be re-posted.
func bookHappyPathSlot(t *testing.T, e *env, base string, monday time.Time, consent anyMap) anyMap {
	t.Helper()
	// The happy path: 201 with the slot and NOTHING else.
	booking := anyMap{
		"start": monday.Add(1 * time.Hour), "end": monday.Add(90 * time.Minute),
		"subject": "Intro call",
		"booker":  anyMap{"name": "Anna Anonymous", "email": "anna@visitor.example"},
		"consent": consent,
	}
	var confirmation map[string]any
	if status := publicCall(t, e, "POST", base, booking, nil, &confirmation); status != http.StatusCreated {
		t.Fatalf("booking → %d %v", status, confirmation)
	}
	if len(confirmation) != 2 || confirmation["start"] == nil || confirmation["end"] == nil {
		t.Fatalf("confirmation discloses more than the slot: %v", confirmation)
	}

	// The booker exists once; a second booking re-uses the person.
	second := anyMap{
		"start": monday.Add(3 * time.Hour), "end": monday.Add(3*time.Hour + 30*time.Minute),
		"booker":  anyMap{"name": "Anna Anonymous", "email": "ANNA@visitor.example"},
		"consent": consent,
	}
	if status := publicCall(t, e, "POST", base, second, nil, nil); status != http.StatusCreated {
		t.Fatalf("second booking → %d", status)
	}
	var persons int
	if err := e.owner.QueryRow(context.Background(), `SELECT count(*) FROM person`).Scan(&persons); err != nil {
		t.Fatal(err)
	}
	if persons != 1 {
		t.Fatalf("idempotent-on-email booker landed as %d persons, want 1", persons)
	}
	return booking
}

// assertBookingProofAndProvenance checks the consent proof carries the
// passthrough verbatim under the system principal, and the meeting's
// provenance is the public surface — never "manual".
func assertBookingProofAndProvenance(t *testing.T, e *env) {
	t.Helper()
	// The consent proof carries the passthrough verbatim, attributed to
	// the system principal; the audit stream owns the whole capture.
	var policyVersion, policyText, source, actorType string
	if err := e.owner.QueryRow(context.Background(), `
		SELECT policy_version, policy_text, source,
		       (SELECT actor_type FROM audit_log WHERE action = 'consent_grant' LIMIT 1)
		FROM consent_event LIMIT 1`).Scan(&policyVersion, &policyText, &source, &actorType); err != nil {
		t.Fatal(err)
	}
	if policyVersion != "pp-2026-01" || policyText != "You agree we may contact you about this meeting." ||
		source != "public_booking" || actorType != "system" {
		t.Fatalf("consent proof lost the passthrough: version=%q text=%q source=%q actor=%q",
			policyVersion, policyText, source, actorType)
	}

	// The meeting's provenance is the public surface, never "manual".
	var activitySource string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT source FROM activity WHERE kind = 'meeting' LIMIT 1`).Scan(&activitySource); err != nil {
		t.Fatal(err)
	}
	if activitySource != "public_booking" {
		t.Fatalf("public booking captured as source=%q — a stranger's submission must not read as hand-entered", activitySource)
	}
}

// assertWithdrawalStandsAgainstBooking covers the consent-hijack guard:
// a withdrawal on record STANDS — an anonymous booking naming the same
// email may proceed but cannot flip the state back to granted.
func assertWithdrawalStandsAgainstBooking(t *testing.T, e *env, base string, monday time.Time, purposeID string, consent anyMap) {
	t.Helper()
	var annaID string
	if err := e.owner.QueryRow(context.Background(), `SELECT id FROM person`).Scan(&annaID); err != nil {
		t.Fatal(err)
	}
	if status := e.call(t, "POST", "/v1/people/"+annaID+"/consent", anyMap{
		"purpose_id": purposeID, "new_state": "withdrawn",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("withdraw → %d", status)
	}
	fourth := anyMap{
		"start": monday.Add(7 * time.Hour), "end": monday.Add(7*time.Hour + 30*time.Minute),
		"booker":  anyMap{"name": "Anna Anonymous", "email": "anna@visitor.example"},
		"consent": consent,
	}
	if status := publicCall(t, e, "POST", base, fourth, nil, nil); status != http.StatusCreated {
		t.Fatalf("booking after withdrawal → %d (the booking may proceed; the consent flip may not)", status)
	}
	var stateAfter string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT state FROM person_consent WHERE person_id = $1 AND purpose_id = $2`,
		annaID, purposeID).Scan(&stateAfter); err != nil {
		t.Fatal(err)
	}
	if stateAfter != "withdrawn" {
		t.Fatalf("anonymous booking flipped a withdrawal to %q — consent hijack", stateAfter)
	}
}

// assertIdempotencyKeyReplay checks a keyed replay returns the recorded
// confirmation without landing a second meeting.
func assertIdempotencyKeyReplay(t *testing.T, e *env, base string, monday time.Time, consent anyMap) {
	t.Helper()
	replayKey := map[string]string{"Idempotency-Key": "public-replay-1"}
	third := anyMap{
		"start": monday.Add(5 * time.Hour), "end": monday.Add(5*time.Hour + 30*time.Minute),
		"booker":  anyMap{"name": "Anna Anonymous", "email": "anna@visitor.example"},
		"consent": consent,
	}
	if status := publicCall(t, e, "POST", base, third, replayKey, nil); status != http.StatusCreated {
		t.Fatalf("keyed booking → %d", status)
	}
	if status := publicCall(t, e, "POST", base, third, replayKey, nil); status != http.StatusCreated {
		t.Fatalf("keyed replay → %d, want the recorded 201", status)
	}
	var meetings int
	if err := e.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM activity WHERE kind = 'meeting'`).Scan(&meetings); err != nil {
		t.Fatal(err)
	}
	if meetings != 4 {
		t.Fatalf("%d meetings landed, want 4 (the replay applied nothing)", meetings)
	}
}

func TestPublicBookingEndToEnd(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	slug := bookingSlug(t, e)
	monday := nextMonday()
	purposeID := seededTransactionalPurposeID(t, e)

	base := "/v1/public/booking/" + slug
	window := fmt.Sprintf("?from=%s&to=%s", monday.Format(time.RFC3339), monday.Add(8*time.Hour).Format(time.RFC3339))

	assertAnonymousAvailability(t, e, base, window)
	assertBookingRequiresValidConsent(t, e, base, monday)

	consent := anyMap{"purpose_id": purposeID, "policy_version": "pp-2026-01", "wording": "You agree we may contact you about this meeting."}
	booking := bookHappyPathSlot(t, e, base, monday, consent)
	assertBookingProofAndProvenance(t, e)
	assertWithdrawalStandsAgainstBooking(t, e, base, monday, purposeID, consent)

	// The taken slot answers slot_taken.
	var problem struct {
		Code string `json:"code"`
	}
	if status := publicCall(t, e, "POST", base, booking, nil, &problem); status != http.StatusConflict || problem.Code != "slot_taken" {
		t.Fatalf("double-book → %d %q, want 409 slot_taken", status, problem.Code)
	}

	assertIdempotencyKeyReplay(t, e, base, monday, consent)
}

// The anonymous surface is throttled per slug: a flood of booking posts
// meets 429 long before it meets the calendar.
func TestPublicBookingRateLimited(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	slug := bookingSlug(t, e)

	last := 0
	for i := 0; i < 21; i++ {
		last = publicCall(t, e, "POST", "/v1/public/booking/"+slug, anyMap{}, nil, nil)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("21st burst booking → %d, want 429", last)
	}
}
