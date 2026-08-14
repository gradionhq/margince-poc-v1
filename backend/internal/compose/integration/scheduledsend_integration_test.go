// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A message a rep chose to send later, over the real composition (ADR-0104).
//
// Three facts live here that nothing shorter can prove, because each is about
// what does NOT exist.
//
// THE TIMELINE STAYS SILENT. A scheduled message writes no activity and no
// delivery row until it fires. Row counts before and after are the only honest
// way to show an absence; a handler test would only show that one endpoint
// returned a 201.
//
// FIRING IS THE ORDINARY SEND. The activity, the delivery and the dispatch job
// appear together at fire, produced by the same store method an immediate send
// runs — so the assertion is that the fired message is indistinguishable from
// one sent by hand at that moment.
//
// A REFUSAL HOLDS. Consent withdrawn between scheduling and firing must stop
// the send. That gate is SQL, and a unit test with a stub gate would pass while
// the real one let the message through.
//
// On the double-send cases, mutation-checked: the message is guarded three
// times over — the worker's pre-read, the claim's own status filter, and the
// release CAS — and removing any ONE of them leaves these tests green, because
// the other two still refuse. Removing all three fails them. That redundancy is
// deliberate for an irreversible act, and is recorded here so a later reader
// does not mistake a single guard for the only thing holding the line, or read
// one passing test as proof that the guard they just deleted was dead.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// uuidOf parses a harness-held id, failing the test rather than the assertion.
func uuidOf(t *testing.T, raw string) ids.UUID {
	t.Helper()
	id, err := ids.Parse(raw)
	if err != nil {
		t.Fatalf("id %q: %v", raw, err)
	}
	return id
}

// scheduleFor issues a deferred send and returns the scheduled record's id.
func (p *preflightEnv) scheduleFor(t *testing.T, at time.Time) ids.UUID {
	t.Helper()
	var scheduled struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	status := p.Call(t, "POST", "/v1/activities/"+p.activityID+"/send-email", apptest.AnyMap{
		"subject": "Monday morning", "body": "Written the night before.",
		"to": []string{"buyer@preflight.test"}, "consent_purpose": "transactional",
		"scheduled_at": at.UTC().Format(time.RFC3339),
		"scheduled_tz": "Europe/Berlin",
	}, nil, &scheduled)
	if status != http.StatusCreated {
		t.Fatalf("scheduling a send → %d, want 201 (a scheduled message is a new record, not an accepted send)", status)
	}
	if scheduled.Status != activities.ScheduledStatusScheduled {
		t.Fatalf("a freshly scheduled message reads %q, want %q", scheduled.Status, activities.ScheduledStatusScheduled)
	}
	id, err := ids.Parse(scheduled.ID)
	if err != nil {
		t.Fatalf("scheduling returned no id: %v", err)
	}
	return id
}

// scheduledStatus reads one scheduled row's state and hold reason.
func (p *preflightEnv) scheduledStatus(t *testing.T, id ids.UUID) (string, string) {
	t.Helper()
	var (
		status string
		reason *string
	)
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT status, held_reason FROM scheduled_send WHERE id = $1`, id).Scan(&status, &reason)
	}); err != nil {
		t.Fatalf("reading the scheduled send: %v", err)
	}
	if reason == nil {
		return status, ""
	}
	return status, *reason
}

// countDeliveries counts staged deliveries, which is how "nothing was handed to
// the machinery" is stated as a fact rather than an assumption.
func (p *preflightEnv) countDeliveries(t *testing.T) int {
	t.Helper()
	var n int
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM comms_outbound`).Scan(&n)
	}); err != nil {
		t.Fatalf("counting staged deliveries: %v", err)
	}
	return n
}

// fire drives the REAL timer worker for one message — the production object,
// assembled the way the worker role assembles it, so the authority rebuild, the
// gate re-run and the fire transaction are all the ones that ship.
//
// It returns whether the message was sent, read from the row afterwards rather
// than from the worker: the row is what the rest of the product sees.
func (p *preflightEnv) fire(t *testing.T, id ids.UUID) {
	t.Helper()
	ws := p.workspaceID(t)
	if err := compose.DriveScheduledSendForTest(context.Background(), p.Pool, ws, id); err != nil {
		t.Fatalf("driving the scheduled-send timer: %v", err)
	}
}

// sent reports whether a scheduled message reached the delivery machinery.
func (p *preflightEnv) sent(t *testing.T, id ids.UUID) bool {
	t.Helper()
	status, _ := p.scheduledStatus(t, id)
	return status == activities.ScheduledStatusReleased
}

// makeDue moves a message's moment into the past so its alarm is ripe. The
// alternative is sleeping, which makes a suite slow and flaky at once.
func (p *preflightEnv) makeDue(t *testing.T, id ids.UUID) {
	t.Helper()
	p.setDueAt(t, id, time.Now().Add(-time.Minute))
}

// setDueAt places a message's moment exactly, for the cases about lateness.
func (p *preflightEnv) setDueAt(t *testing.T, id ids.UUID, at time.Time) {
	t.Helper()
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE scheduled_send SET scheduled_at = $1 WHERE id = $2`, at.UTC(), id)
		return err
	}); err != nil {
		t.Fatalf("moving the scheduled moment: %v", err)
	}
}

// withdrawConsent revokes the recipient's grant for the purpose the scheduled
// message was written under, through the real consent surface.
func (p *preflightEnv) withdrawConsent(t *testing.T) {
	t.Helper()
	var purposes struct {
		Data []struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if status := p.Call(t, "GET", "/v1/consent-purposes", nil, nil, &purposes); status != http.StatusOK {
		t.Fatalf("list purposes → %d", status)
	}
	var transactional string
	for _, purpose := range purposes.Data {
		if purpose.Key == "transactional" {
			transactional = purpose.ID
		}
	}
	if transactional == "" {
		t.Fatalf("bootstrap seeded no transactional purpose: %+v", purposes.Data)
	}
	if status := p.Call(t, "POST", "/v1/people/"+p.personID+"/consent", apptest.AnyMap{
		"purpose_id": transactional, "new_state": "withdrawn", "lawful_basis": "consent",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("withdrawing consent → %d", status)
	}
}

func TestAScheduledMessageWritesNothingUntilItFires(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	activitiesBefore := p.countActivities(t, "true")
	deliveriesBefore := p.countDeliveries(t)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))

	// The whole point of the design: a message nobody has sent has no presence
	// on the timeline and nothing queued to carry it.
	if got := p.countActivities(t, "true"); got != activitiesBefore {
		t.Fatalf("scheduling wrote %d activities; a message nobody has sent must write none", got-activitiesBefore)
	}
	if got := p.countDeliveries(t); got != deliveriesBefore {
		t.Fatalf("scheduling staged %d deliveries; nothing may reach the machinery until it fires", got-deliveriesBefore)
	}

	// Its moment arrives.
	p.makeDue(t, id)
	p.fire(t, id)
	if !p.sent(t, id) {
		status, reason := p.scheduledStatus(t, id)
		t.Fatalf("firing a due message did not send it: %q/%q", status, reason)
	}

	if got := p.countActivities(t, "true"); got != activitiesBefore+1 {
		t.Fatalf("firing produced %d activities, want exactly 1", got-activitiesBefore)
	}
	if got := p.countDeliveries(t); got != deliveriesBefore+1 {
		t.Fatalf("firing staged %d deliveries, want exactly 1", got-deliveriesBefore)
	}
	if status, _ := p.scheduledStatus(t, id); status != activities.ScheduledStatusReleased {
		t.Fatalf("a fired message reads %q, want %q — 'sent' would claim the provider was called, and it has not been",
			status, activities.ScheduledStatusReleased)
	}
}

func TestConsentWithdrawnBeforeFiringHoldsTheMessage(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))
	deliveriesBefore := p.countDeliveries(t)

	// The recipient changes their mind after the rep scheduled the message.
	p.withdrawConsent(t)

	p.makeDue(t, id)
	p.fire(t, id)
	if p.sent(t, id) {
		t.Fatal("a message whose recipient withdrew consent was transmitted — the fire-time gate did not run")
	}
	status, reason := p.scheduledStatus(t, id)
	if status != activities.ScheduledStatusHeld {
		t.Fatalf("a refused message reads %q, want %q", status, activities.ScheduledStatusHeld)
	}
	if reason != activities.HeldConsentWithdrawn {
		t.Fatalf("held for %q, want %q — the rep has to be told which gate refused", reason, activities.HeldConsentWithdrawn)
	}
	if got := p.countDeliveries(t); got != deliveriesBefore {
		t.Fatalf("a held message staged %d deliveries; a refusal must reach the machinery not at all", got-deliveriesBefore)
	}
}

func TestAMessageFiredTooLateIsHeldRatherThanSent(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))

	// Its moment came and went while nothing was running.
	p.setDueAt(t, id, time.Now().Add(-4*time.Hour))
	p.fire(t, id)

	if p.sent(t, id) {
		t.Fatal("a message four hours past its moment was still transmitted — mail timed for 09:00 must not arrive at 13:00")
	}
	status, reason := p.scheduledStatus(t, id)
	if status != activities.ScheduledStatusHeld || reason != activities.HeldMissedWindow {
		t.Fatalf("a missed message reads %q/%q, want %q/%q",
			status, reason, activities.ScheduledStatusHeld, activities.HeldMissedWindow)
	}
}

func TestACancelledMessageIsNotSentWhenItsTimerFires(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))
	activitiesBefore := p.countActivities(t, "true")

	if status := p.Call(t, "POST", "/v1/scheduled-sends/"+id.String()+"/cancel", nil, nil, nil); status != http.StatusNoContent {
		t.Fatalf("cancelling → %d, want 204", status)
	}

	// The alarm still rings: cancelling does not chase the job down, because a
	// row that is no longer pending is the whole answer.
	p.makeDue(t, id)
	p.fire(t, id)
	if p.sent(t, id) {
		t.Fatal("a cancelled message was transmitted when its timer fired")
	}
	if got := p.countActivities(t, "true"); got != activitiesBefore {
		t.Fatalf("firing a cancelled message wrote %d activities, want none", got-activitiesBefore)
	}
	if status, _ := p.scheduledStatus(t, id); status != activities.ScheduledStatusCancelled {
		t.Fatalf("a cancelled message reads %q after its timer fired, want %q", status, activities.ScheduledStatusCancelled)
	}
}

func TestErasingARecipientEmptiesAndStopsTheirScheduledMail(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	// A message written the night before, addressed to the person who is about
	// to exercise Art. 17.
	id := p.scheduleFor(t, time.Now().Add(12*time.Hour))

	personID, err := ids.Parse(p.personID)
	if err != nil {
		t.Fatalf("person id %q: %v", p.personID, err)
	}
	// Run under the bootstrapped admin this harness already signed in, rather
	// than a synthetic principal: the eraser's own gates are part of what this
	// asserts, and a hand-built unbounded actor would walk past them.
	admin := principal.WithActor(
		principal.WithCorrelationID(principal.WithWorkspaceID(context.Background(), p.workspaceID(t)), ids.NewV7()),
		principal.Principal{
			Type: principal.PrincipalHuman, ID: "human:" + p.user,
			UserID:   uuidOf(t, p.user),
			SeatType: principal.SeatFull,
			Permissions: principal.Permissions{
				RoleKeys: []string{"admin"},
				Objects: map[string]principal.ObjectGrant{
					"person":   {Create: true, Read: true, Update: true, Delete: true},
					"activity": {Create: true, Read: true, Update: true, Delete: true},
				},
			},
		})
	if err := privacy.NewEraser(compose.InstallationDB(p.Pool)).ErasePerson(
		admin, personID, "art-17"); err != nil {
		t.Fatalf("erasing the recipient: %v", err)
	}

	// The payload must no longer name them, and the message must no longer be
	// waiting to go out: a scheduled row survives with a live timer, so an
	// emptied-but-pending one would still fire the morning after the erasure
	// certified this person's data destroyed.
	status, _ := p.scheduledStatus(t, id)
	if status != activities.ScheduledStatusCancelled {
		t.Fatalf("a scheduled message to an erased person reads %q, want %q — it still has a timer",
			status, activities.ScheduledStatusCancelled)
	}
	var payload string
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT payload::text FROM scheduled_send WHERE id = $1`, id).Scan(&payload)
	}); err != nil {
		t.Fatalf("reading the frozen payload: %v", err)
	}
	if strings.Contains(payload, "buyer@preflight.test") {
		t.Fatalf("the erased person's address survives in a scheduled message: %s", payload)
	}
	if strings.Contains(payload, "Written the night before.") {
		t.Fatalf("the body of a message to an erased person survives: %s", payload)
	}
}

func TestTwoTimersFiringTheSameMessageSendItOnce(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))
	activitiesBefore := p.countActivities(t, "true")
	p.makeDue(t, id)

	// Rescheduling enqueues a FRESH alarm and deliberately leaves the old one
	// live, so two timers for one message is the ordinary case, not an edge.
	p.fire(t, id)
	if !p.sent(t, id) {
		t.Fatal("the first timer did not send the message")
	}
	p.fire(t, id)
	if got := p.countActivities(t, "true"); got != activitiesBefore+1 {
		t.Fatalf("two timers produced %d activities, want exactly 1", got-activitiesBefore)
	}
}
