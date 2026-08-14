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
	"fmt"
	"net/http"
	"slices"
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

// seedConsentedRecipient creates a person with one address and a granted
// transactional purpose — the minimum a send's consent gate demands of an
// addressee, spelled once because a multi-recipient fixture needs it per head.
func (p *preflightEnv) seedConsentedRecipient(t *testing.T, name, email string) {
	t.Helper()
	var person struct {
		ID string `json:"id"`
	}
	if status := p.Call(t, "POST", "/v1/people", apptest.AnyMap{
		"full_name": name,
		"emails":    []apptest.AnyMap{{"email": email}},
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create %s → %d", email, status)
	}
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
	if status := p.Call(t, "POST", "/v1/people/"+person.ID+"/consent", apptest.AnyMap{
		"purpose_id": transactional, "new_state": "granted", "lawful_basis": "contract",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("grant consent for %s → %d", email, status)
	}
}

// privacyAdmin binds the context both privileged privacy paths demand: a HUMAN
// holding person.delete. Erasure and the subject-access export ask the same
// trust level on purpose — one destroys the data and the other discloses all of
// it — so the two tests share one spelling of it rather than each inventing a
// principal that walks past the gates they are supposed to exercise.
func (p *preflightEnv) privacyAdmin(t *testing.T) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(context.Background(), p.workspaceID(t))
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + p.user,
		UserID:   uuidOf(t, p.user),
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"admin"},
			// The export deliberately crosses the caller's row scope: Art. 15
			// owes the subject everything held, not the slice one rep can see,
			// so a bounded caller is refused outright.
			RowScope: principal.RowScopeAll,
			Objects: map[string]principal.ObjectGrant{
				"person":   {Create: true, Read: true, Update: true, Delete: true},
				"activity": {Create: true, Read: true, Update: true, Delete: true},
			},
		},
	})
}

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
	p.setTransactionalConsent(t, "withdrawn")
}

// setTransactionalConsent moves the recipient's transactional grant either way,
// through the real consent surface.
func (p *preflightEnv) setTransactionalConsent(t *testing.T, state string) {
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
		"purpose_id": transactional, "new_state": state, "lawful_basis": "consent",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("setting consent to %s → %d", state, status)
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

// DRAFT-AC-N-10a. Firing hands the message to the delivery machinery and stops
// at `released`, which is honest at that instant — the provider has not been
// called. When it IS called and confirms, the scheduled row has to follow: a
// message this system sent reads "sent" whether a rep scheduled it or pressed
// the button. Anything else leaves a rep looking at a message that demonstrably
// arrived while its record still says it was merely handed over.
func TestAConfirmedReceiptCarriesTheScheduledSendToSent(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))
	p.makeDue(t, id)
	p.fire(t, id)

	// Handed over, not yet delivered. This is the state the fire leaves behind.
	if status, _ := p.scheduledStatus(t, id); status != activities.ScheduledStatusReleased {
		t.Fatalf("a fired message reads %q, want %q before the provider is called",
			status, activities.ScheduledStatusReleased)
	}

	// A real dispatch through the real connector to a real receipt.
	activityID := p.releasedActivity(t, id)
	deliveryID, _ := p.deliveryFor(t, activityID)
	p.transmit(t, deliveryID, "")

	if status := p.scheduledStatusThroughAPI(t, id); status != activities.ScheduledStatusSent {
		t.Fatalf("after the provider confirmed receipt the message reads %q, want %q — a rep would be looking at a message that has arrived while its record says it was only handed over",
			status, activities.ScheduledStatusSent)
	}
}

// The filter has to read the SAME state the projection renders. A derived
// status rendered on the way out while the raw column is filtered on the way in
// is the shape where `?status=sent` returns nothing and `?status=released`
// returns rows that read "sent" — each half correct, the pair useless.
func TestFilteringByStatusFindsTheStateTheListActuallyShows(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))
	p.makeDue(t, id)
	p.fire(t, id)
	activityID := p.releasedActivity(t, id)
	deliveryID, _ := p.deliveryFor(t, activityID)
	p.transmit(t, deliveryID, "")

	// The list renders it as sent, so the sent filter must find it.
	if got := p.listScheduledIDs(t, activities.ScheduledStatusSent); !slices.Contains(got, id.String()) {
		t.Errorf("?status=sent did not return the message the list renders as sent: %v", got)
	}
	// …and the released filter must not, because no rep sees it that way.
	if got := p.listScheduledIDs(t, activities.ScheduledStatusReleased); slices.Contains(got, id.String()) {
		t.Errorf("?status=released returned a message the list renders as sent: %v", got)
	}
}

// heldCard is the inbox card as a rep sees it.
type heldCard struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

// heldCardFor finds the card raised for one message, through the endpoint a
// rep's inbox reads — a row written to a table nothing serves is not a card
// anybody sees. Matched on the message named in the payload: the card carries no
// target id, because a held message produced no activity to point at.
func (p *preflightEnv) heldCardFor(t *testing.T, id ids.UUID) (heldCard, bool) {
	t.Helper()
	var page struct {
		Data []struct {
			heldCard
			ProposedChange *struct {
				ScheduledSendID string `json:"scheduled_send_id"`
			} `json:"proposed_change"`
		} `json:"data"`
	}
	if status := p.Call(t, "GET", "/v1/approvals?status=pending", nil, nil, &page); status != http.StatusOK {
		t.Fatalf("reading the approval inbox → %d", status)
	}
	for _, row := range page.Data {
		if row.ProposedChange != nil && row.ProposedChange.ScheduledSendID == id.String() {
			return row.heldCard, true
		}
	}
	return heldCard{}, false
}

// forceStatus moves a scheduled row directly, standing in for whatever else
// could have moved it while a card sat in an inbox.
func (p *preflightEnv) forceStatus(t *testing.T, id ids.UUID, status string) {
	t.Helper()
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE scheduled_send SET status = $1, held_reason = NULL WHERE id = $2`, status, id)
		return err
	}); err != nil {
		t.Fatalf("moving the scheduled send to %s: %v", status, err)
	}
}

// grantTransactionalConsent restores the grant the withdrawal removed, so a rep
// accepting a held card has actually fixed what stopped it.
func (p *preflightEnv) grantTransactionalConsent(t *testing.T) {
	t.Helper()
	p.setTransactionalConsent(t, "granted")
}

// releasedActivity reads the activity a fired scheduled send produced.
func (p *preflightEnv) releasedActivity(t *testing.T, id ids.UUID) ids.UUID {
	t.Helper()
	var activityID ids.UUID
	if err := apptest.InWorkspace(p.AppEnv, t, p.Slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT activity_id FROM scheduled_send WHERE id = $1`, id).Scan(&activityID)
	}); err != nil {
		t.Fatalf("reading the activity the fire produced: %v", err)
	}
	return activityID
}

// scheduledStatusThroughAPI reads the status the way a REP sees it — through the
// endpoint, where the derived state is computed. scheduledStatus reads the raw
// column, which stays 'released': the difference between the two IS the
// behaviour under test.
func (p *preflightEnv) scheduledStatusThroughAPI(t *testing.T, id ids.UUID) string {
	t.Helper()
	var got struct {
		Status string `json:"status"`
	}
	if status := p.Call(t, "GET", "/v1/scheduled-sends/"+id.String(), nil, nil, &got); status != http.StatusOK {
		t.Fatalf("reading the scheduled send → %d", status)
	}
	return got.Status
}

// listScheduledIDs reads the rep's list filtered by one status, through the
// endpoint rather than the table: the filter and the projection are the two
// halves this asserts agree.
func (p *preflightEnv) listScheduledIDs(t *testing.T, status string) []string {
	t.Helper()
	var rows []struct {
		ID string `json:"id"`
	}
	if code := p.Call(t, "GET", "/v1/scheduled-sends?status="+status, nil, nil, &rows); code != http.StatusOK {
		t.Fatalf("listing scheduled sends with status=%s → %d", status, code)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
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

// DRAFT-AC-N-11a. A message the system stopped is a decision waiting for a rep,
// and a state they must go looking for is one they find late. It has to reach
// the inbox this product already uses for "something needs you".
//
// Visibility is only half. The card carries the same Accept/Reject buttons every
// other card carries, and if they did nothing a rep would click one, watch the
// card vanish, and still have a message sitting stopped — a decision reported
// but never made. So this asserts the ACTION, not the appearance.
func TestARepCanRetryAHeldMessageFromTheirInbox(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))
	p.withdrawConsent(t)
	p.makeDue(t, id)
	p.fire(t, id)

	if status, _ := p.scheduledStatus(t, id); status != activities.ScheduledStatusHeld {
		t.Fatalf("the message reads %q, want %q — this test is about what a hold raises", status, activities.ScheduledStatusHeld)
	}
	card, found := p.heldCardFor(t, id)
	if !found {
		t.Fatal("a message stopped and no card appeared — a rep would only find out by going looking")
	}
	if !strings.Contains(card.Summary, "Monday morning") || !strings.Contains(strings.ToLower(card.Summary), "consent") {
		t.Errorf("the card does not say which message stopped or why: %q", card.Summary)
	}

	// The rep fixes the problem and clicks Accept.
	p.grantTransactionalConsent(t)
	if status := p.Call(t, "POST", "/v1/approvals/"+card.ID+"/approve", apptest.AnyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("accepting the card → %d, want 200", status)
	}

	// Accept has to DO something: the message is armed again, not merely
	// dismissed from the list.
	status, reason := p.scheduledStatus(t, id)
	if status != activities.ScheduledStatusScheduled {
		t.Fatalf("after Accept the message reads %q/%q, want %q — the card was dismissed and the message left stopped",
			status, reason, activities.ScheduledStatusScheduled)
	}
	if _, still := p.heldCardFor(t, id); still {
		t.Error("the card outlived the rep's answer")
	}
}

// The other button. Reject means "give up on this one", and without a declined
// effect the card would leave the inbox while the message waited forever for a
// decision nobody would make again.
func TestARepCanAbandonAHeldMessageFromTheirInbox(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))
	p.withdrawConsent(t)
	p.makeDue(t, id)
	p.fire(t, id)

	card, found := p.heldCardFor(t, id)
	if !found {
		t.Fatal("no card to reject — this test is about rejecting one")
	}
	if status := p.Call(t, "POST", "/v1/approvals/"+card.ID+"/reject", apptest.AnyMap{}, nil, nil); status != http.StatusOK {
		t.Fatalf("rejecting the card → %d, want 200", status)
	}

	if status, _ := p.scheduledStatus(t, id); status != activities.ScheduledStatusCancelled {
		t.Fatalf("after Reject the message reads %q, want %q — the card was dismissed and the message left stopped",
			status, activities.ScheduledStatusCancelled)
	}
}

// Reject and the cancellation it releases commit together, or neither does.
//
// The failure this closes is specific: reject the card, have the cancellation
// fail afterwards, and the card is already rejected — a retry is refused as
// already-decided while the message is still held. The rep answered, the system
// recorded the answer, and nothing happened.
//
// Driven by rejecting a card whose message was cancelled out from under it: the
// cancel then finds no pending row and fails, which must take the rejection down
// with it rather than leaving a decided card over a message in the wrong state.
func TestARejectionThatCannotCancelLeavesTheCardRetryable(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	id := p.scheduleFor(t, time.Now().Add(2*time.Hour))
	p.withdrawConsent(t)
	p.makeDue(t, id)
	p.fire(t, id)

	card, found := p.heldCardFor(t, id)
	if !found {
		t.Fatal("no card raised — this test is about rejecting one")
	}

	// The message reaches a state the cancel cannot act on, behind the card's
	// back — a rep on another device, or a sweep. Cancelled rather than
	// released: the state-shape CHECK requires a released row to name the
	// activity it produced, and inventing one would be a fixture the writer
	// never produces.
	p.forceStatus(t, id, activities.ScheduledStatusCancelled)

	// The rejection must now fail as a whole rather than commit half of itself.
	status := p.Call(t, "POST", "/v1/approvals/"+card.ID+"/reject", apptest.AnyMap{}, nil, nil)
	if status == http.StatusOK {
		t.Fatal("the rejection reported success while its cancellation could not run — the card would be decided and the message left in the wrong state")
	}

	// And the card is still there to try again, because the decision rolled back
	// with the work.
	if _, still := p.heldCardFor(t, id); !still {
		t.Error("the card was consumed by a rejection that did no work — a retry would be refused as already decided")
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
	if err := privacy.NewEraser(compose.InstallationDB(p.Pool)).ErasePerson(
		p.privacyAdmin(t), personID, "art-17"); err != nil {
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

// Art. 15 owes the subject the data held about them, and a message somebody
// wrote to them and has not sent is data held about them. It reaches the export
// by a route the sent-message projection cannot take: a scheduled send has no
// activity and no delivery row, so all three of that query's clauses miss it.
func TestASubjectAccessExportCarriesTheMailNobodyHasSentYet(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	// Scheduled through the real endpoint, so the row under test is the one the
	// product writes — payload shape included, which is what the export reads.
	p.scheduleFor(t, time.Now().Add(6*time.Hour))

	personID, err := ids.Parse(p.personID)
	if err != nil {
		t.Fatalf("person id %q: %v", p.personID, err)
	}
	pkg, err := privacy.AssembleSAR(
		p.privacyAdmin(t), compose.InstallationDB(p.Pool), ids.From[ids.PersonKind](personID))
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}

	if len(pkg.ScheduledMessages) != 1 {
		t.Fatalf("the export carried %d unsent messages, want the one waiting for this person: %#v",
			len(pkg.ScheduledMessages), pkg.ScheduledMessages)
	}
	row := pkg.ScheduledMessages[0]
	if subject, _ := row["subject"].(string); subject != "Monday morning" {
		t.Errorf("the unsent message came back with subject %q, want the one that was scheduled", subject)
	}
	if body, _ := row["body"].(string); !strings.Contains(body, "Written the night before") {
		t.Errorf("the export withheld the body of a message written to this person: %#v", row)
	}
	// The state is part of the answer: a subject told a message exists, but not
	// whether it is still going to arrive, has been told half a fact.
	if status, _ := row["status"].(string); status != activities.ScheduledStatusScheduled {
		t.Errorf("the export reported status %q, want %q", status, activities.ScheduledStatusScheduled)
	}
}

// The blind-copy rule has two halves that pull opposite ways, and only a
// message with SEVERAL blind recipients can show both: a bcc'd subject must
// find their own message in their export, and must not learn who else was
// blind-copied on it. A projection that satisfied one half would look correct
// against a single-recipient fixture.
func TestABlindCopiedSubjectSeesTheirOwnMailAndNobodyElsesAddress(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	// The subject is BLIND-copied; somebody else is the visible addressee, and
	// a third party shares the blind list with them.
	//
	// Both extra addressees need a person on file and a granted purpose,
	// because consent is owed to EVERY addressee however they were addressed —
	// the same rule that makes a blind copy a consent question at all. Without
	// them the send is refused 409 before it can be scheduled, which would say
	// nothing about the export.
	//
	// The other blind address is typed in MIXED CASE on purpose. The send path
	// removes blind copies from the To line case-insensitively; an export that
	// compared raw would leave this one sitting in the visible list while the
	// message itself correctly hid it, and the two derivations of "who is on
	// the To line" would disagree about the same message.
	const otherBlind = "Third.Party@Preflight.test"
	p.seedConsentedRecipient(t, "Visible Addressee", "visible@preflight.test")
	p.seedConsentedRecipient(t, "Other Blind", otherBlind)
	var scheduled struct {
		ID string `json:"id"`
	}
	status := p.Call(t, "POST", "/v1/emails", apptest.AnyMap{
		"subject": "Quiet copy", "body": "You were blind-copied on this.",
		"to":              []string{"visible@preflight.test"},
		"bcc":             []string{"buyer@preflight.test", otherBlind},
		"consent_purpose": "transactional",
		"links": []apptest.AnyMap{
			{"entity_type": "person", "entity_id": p.personID},
		},
		"scheduled_at": time.Now().Add(6 * time.Hour).UTC().Format(time.RFC3339),
		"scheduled_tz": "Europe/Berlin",
	}, nil, &scheduled)
	if status != http.StatusCreated {
		t.Fatalf("scheduling a blind-copied message → %d, want 201", status)
	}

	personID, err := ids.Parse(p.personID)
	if err != nil {
		t.Fatalf("person id %q: %v", p.personID, err)
	}
	pkg, err := privacy.AssembleSAR(
		p.privacyAdmin(t), compose.InstallationDB(p.Pool), ids.From[ids.PersonKind](personID))
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}

	// Found on the blind list: without this the subject is absent from their own
	// export of a message they were going to receive.
	if len(pkg.ScheduledMessages) != 1 {
		t.Fatalf("a blind-copied subject got %d unsent messages, want 1: %#v",
			len(pkg.ScheduledMessages), pkg.ScheduledMessages)
	}
	// …and narrowed to themselves: exporting the whole blind list would hand
	// this subject a stranger's address, which is what a blind copy exists to
	// prevent.
	rendered := fmt.Sprintf("%#v", pkg.ScheduledMessages[0])
	if !strings.Contains(rendered, "buyer@preflight.test") {
		t.Errorf("the export withheld the subject's own blind address: %s", rendered)
	}
	// Case-insensitive, because the leak this guards against does not care how
	// the address was typed.
	if strings.Contains(strings.ToLower(rendered), strings.ToLower(otherBlind)) {
		t.Errorf("the export disclosed another blind recipient's address to this subject: %s", rendered)
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
