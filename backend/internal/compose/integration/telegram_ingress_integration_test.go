// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The ingress half of the Telegram acceptance suite (telegram-oa design §12):
// what an inbound message leaves behind once the real webhook has accepted it
// and the real worker has normalized it. The fixture is in
// telegram_fixture_integration_test.go.
//
// Every test here drives the whole leg — composed router → raw_capture →
// River → the ONE guarded Sink → people — because each claim is about the
// SEAM: that an unmatched sender lands ownerless, that a redelivery converges,
// that the mail ladder never judges a message with no address, and that the
// capture completes on the async path rather than at the ack.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// telegramProviderDate is the unix-seconds instant every fixture update
// carries as Telegram's own send time. It is INJECTED rather than taken from
// the clock so a captured activity's occurred_at is a fixed, asserted value —
// the customer's send time is neither of this system's two anchors, and a test
// that let it drift could not tell the three apart.
const telegramProviderDate = int64(1785000000)

// captureLatencyBudget is AC7.1's ceiling on the capture window — the
// receipt-to-capture pair the latency test below proves the measurement has to
// be taken over.
const captureLatencyBudget = 60 * time.Second

// capturedMessage reads back what one delivered update became: the activity
// under its chat-scoped natural key, and the person the ensure linked it to.
// Both must exist — an activity with no link is the connector's own retry
// marker, not a captured conversation.
func (c *telegramEnv) capturedMessage(t *testing.T, u telegramUpdate) (activityID, personID string) {
	t.Helper()
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT a.id::text, l.person_id::text
			  FROM activity a
			  JOIN activity_link l ON l.activity_id = a.id AND l.entity_type = 'person'
			 WHERE a.source_system = 'telegram' AND a.source_id = $1`, u.naturalKey()).
			Scan(&activityID, &personID)
	}); err != nil {
		t.Fatalf("reading back the captured message %s: %v", u.naturalKey(), err)
	}
	return activityID, personID
}

// telegramActivities counts the Telegram activities captured under one natural
// key — the number AC-TG-4 is about.
func (c *telegramEnv) telegramActivities(t *testing.T, u telegramUpdate) int {
	t.Helper()
	return c.count(t,
		`SELECT count(*) FROM activity WHERE source_system = 'telegram' AND source_id = $1`, u.naturalKey())
}

// channelIdentities counts the live bindings for one Telegram account.
func (c *telegramEnv) channelIdentities(t *testing.T, u telegramUpdate) int {
	t.Helper()
	return c.count(t, `
		SELECT count(*) FROM person_channel_identity
		 WHERE provider = 'telegram' AND channel_user_id = $1 AND archived_at IS NULL`, u.account())
}

// ingestOne delivers an update and runs the async leg to completion, returning
// nothing but the guarantee that the whole path finished. The worker is built
// AFTER the delivery so the job is already queued when it boots, which is what
// makes the completion feed a reliable "the async leg is done" signal.
func (c *telegramEnv) ingestOne(t *testing.T, u telegramUpdate, cfg compose.JobRunnerConfig) {
	t.Helper()
	if status, _ := c.deliver(t, u); status != 200 {
		t.Fatalf("delivering %s → %d, want 200", u.naturalKey(), status)
	}
	runner, sub := newTelegramWorker(t, c, cfg)
	startTelegramWorker(t, runner)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())
}

// TestAC_TG_3_UnknownSenderBecomesOwnerlessWorkspaceVisiblePerson is AC-TG-3:
// an inbound message from an unrecognised Telegram account creates a Person
// carrying ONLY a channel identity, ownerless, and the conversation is visible
// workspace-wide.
//
// The visibility half is held against a control: the SAME reader must be
// refused an owned record. Without that, "the stranger could read it" would
// also pass on a row-scope clause that had stopped filtering anything.
func TestAC_TG_3_UnknownSenderBecomesOwnerlessWorkspaceVisiblePerson(t *testing.T) {
	c := setupTelegramConnected(t)
	u := telegramUpdate{updateID: 5201, messageID: 21, senderID: 770201, username: "annlee", firstName: "Ann", text: "Is this still available?"}

	// An owned record, created before the capture, is the control the
	// visibility assertion below is measured against.
	var owned struct {
		ID string `json:"id"`
	}
	if status := c.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Owned By The Admin", "owner_id": c.admin,
	}, nil, &owned); status != 201 {
		t.Fatalf("seeding the owned control person → %d", status)
	}

	c.ingestOne(t, u, compose.JobRunnerConfig{})

	activityID, personID := c.capturedMessage(t, u)

	var ownerID *string
	var fullName string
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT owner_id::text, full_name FROM person WHERE id = $1`, personID).
			Scan(&ownerID, &fullName)
	}); err != nil {
		t.Fatalf("reading the auto-created person: %v", err)
	}
	if ownerID != nil {
		t.Fatalf("the auto-created person is owned by %s; a workspace bot has no owner to hand the record to (design D2)", *ownerID)
	}
	if fullName != "Ann" {
		t.Fatalf("person full_name = %q, want the name Telegram reported (%q)", fullName, "Ann")
	}

	// Only a channel identity: no address was ever supplied, so a mail
	// satellite here would mean the mail ensure ran on a record it cannot see.
	if n := c.channelIdentities(t, u); n != 1 {
		t.Fatalf("%d live channel identities for account %s, want 1", n, u.account())
	}
	if n := c.count(t, `SELECT count(*) FROM person_email WHERE person_id = $1`, personID); n != 0 {
		t.Errorf("%d email rows beside the channel identity, want 0", n)
	}
	var boundUsername, identitySource, identityCapturedBy string
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT username, source, captured_by FROM person_channel_identity
			 WHERE provider = 'telegram' AND channel_user_id = $1`, u.account()).
			Scan(&boundUsername, &identitySource, &identityCapturedBy)
	}); err != nil {
		t.Fatalf("reading the channel identity: %v", err)
	}
	if boundUsername != u.username {
		t.Errorf("bound username = %q, want %q", boundUsername, u.username)
	}
	// The provenance is the workspace channel, never the admin who ran connect:
	// stamping that admin would make every captured message look like their own
	// row-scoped work.
	if identityCapturedBy != "connector:telegram" || identitySource != "telegram" {
		t.Errorf("identity provenance = %q/%q, want telegram/connector:telegram",
			identitySource, identityCapturedBy)
	}

	c.assertWorkspaceVisible(t, personID, owned.ID, activityID)
}

// assertWorkspaceVisible holds AC-TG-3's second half over the REAL row-scope
// clauses: a human on the tightest scope a seat can hold reads the ownerless
// person and their conversation, and is refused the owned control.
func (c *telegramEnv) assertWorkspaceVisible(t *testing.T, personID, ownedPersonID, activityID string) {
	t.Helper()
	reader := c.strangerRepCtx(t, map[string]principal.ObjectGrant{
		"person": {Read: true}, "activity": {Read: true},
	})
	store := people.NewStore(c.pool)

	shared, err := ids.ParseAs[ids.PersonKind](personID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPerson(reader, shared, storekit.LiveOnly); err != nil {
		t.Fatalf("a rep outside every team cannot read the channel counterparty: %v — "+
			"an ownerless connector record is workspace-shared", err)
	}

	owned, err := ids.ParseAs[ids.PersonKind](ownedPersonID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPerson(reader, owned, storekit.LiveOnly); err == nil {
		t.Fatal("the same reader could also read a person owned by another human — the row scope is not filtering, so the visibility claim above is vacuous")
	}

	// And the conversation itself, not only the record it hangs off: the
	// timeline read walks activity_link, so a message visible only to the
	// connector would leave the shared person with an empty history.
	if n := c.count(t, `
		SELECT count(*) FROM activity_link WHERE activity_id = $1 AND person_id = $2`,
		activityID, personID); n != 1 {
		t.Fatalf("the captured activity is not linked to the counterparty it names")
	}
}

// TestAC_TG_4_RedeliveryYieldsExactlyOneActivity is AC-TG-4 on both layers a
// redelivery can arrive at.
//
// Telegram redelivering the same update_id is the first: raw_capture's
// conflict target refreshes the stored original and River's by-args uniqueness
// drops the second job. But a job is at-least-once quite apart from the
// provider — River retries what it picked up — so the second half puts the
// SAME job back on the queue and lets it run again. The activity's own natural
// key is what has to absorb that, and nothing above it can.
func TestAC_TG_4_RedeliveryYieldsExactlyOneActivity(t *testing.T) {
	c := setupTelegramConnected(t)
	u := telegramUpdate{updateID: 5301, messageID: 31, senderID: 770301, username: "twice", firstName: "Rita", text: "same message"}

	for attempt := range 2 {
		if status, _ := c.deliver(t, u); status != 200 {
			t.Fatalf("delivery attempt %d → %d, want 200 (a redelivery is still accepted)", attempt+1, status)
		}
	}
	if n := c.rawCaptures(t, u.updateID); n != 1 {
		t.Fatalf("%d raw captures for a redelivered update_id, want 1", n)
	}
	if n := c.ingestJobs(t); n != 1 {
		t.Fatalf("%d ingest jobs for a redelivered update_id, want 1", n)
	}

	runner, sub := newTelegramWorker(t, c, compose.JobRunnerConfig{})
	startTelegramWorker(t, runner)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())

	if n := c.telegramActivities(t, u); n != 1 {
		t.Fatalf("%d activities after two deliveries of one update, want exactly 1", n)
	}
	_, personID := c.capturedMessage(t, u)

	// The job-level replay: River's ladder can hand the same job to a worker
	// twice, and the domain natural key is the only thing standing between that
	// and a duplicated conversation.
	if _, err := c.owner.Exec(context.Background(), `
		UPDATE river_job SET state = 'available', finalized_at = NULL, attempt = 0
		 WHERE kind = $1 AND args->>'connection_id' = $2`,
		compose.TelegramIngestArgs{}.Kind(), c.conn.ID.String()); err != nil {
		t.Fatalf("returning the ingest job to the queue: %v", err)
	}
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())

	if n := c.telegramActivities(t, u); n != 1 {
		t.Fatalf("%d activities after the job was worked twice, want exactly 1 — "+
			"the activity's natural key, not the update_id index, is what absorbs a job retry", n)
	}
	if n := c.channelIdentities(t, u); n != 1 {
		t.Fatalf("%d channel identities after the job was worked twice, want 1", n)
	}
	if n := c.count(t, `SELECT count(*) FROM person WHERE archived_at IS NULL AND owner_id IS NULL`); n != 1 {
		t.Fatalf("%d ownerless people after the job was worked twice, want the one counterparty", n)
	}
	if n := c.count(t, `SELECT count(*) FROM activity_link WHERE person_id = $1`, personID); n != 1 {
		t.Fatalf("%d activity links onto the counterparty, want 1", n)
	}
}

// TestAC_TG_5_MailGatesAreNoOpsForAChannelRecord is AC-TG-5: a captured
// channel message carries no mail domain, so every mail-domain gate is a no-op
// and the record still reaches the resolver.
//
// The two halves have to be asserted together. "No gate artifact" alone would
// pass on a record that nothing happened to at all, and "the person exists"
// alone would pass on a ladder that had judged the sender and happened to let
// them through. So this asserts the person AND the absence of every breadcrumb
// the ladder writes — against a composition whose free-mail and
// transactional/ESP registries are deliberately POPULATED, and a workspace
// that owns its own mail domain, so all three gates have something to match on
// if any of them ever reads a channel record's display text as an address.
func TestAC_TG_5_MailGatesAreNoOpsForAChannelRecord(t *testing.T) {
	c := setupTelegramConnected(t)
	// The workspace's own mail domain, so the colleagues gate has something to
	// find if anything asks it about a record with no domain.
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO workspace_email_domain (workspace_id, domain) VALUES ($1, 'own-house.test')`, c.ws)
		return err
	}); err != nil {
		t.Fatalf("seeding the internal mail domain: %v", err)
	}

	// The sender's Telegram profile is deliberately shaped like an address the
	// gates WOULD act on: a free-mail box, a transactional/ESP domain, and the
	// workspace's own house domain, all in the untrusted display text.
	u := telegramUpdate{
		updateID: 5401, messageID: 41, senderID: 770401,
		username: "noreply", firstName: "billing@sendgrid.net", text: "quote please",
	}
	c.ingestOne(t, u, compose.JobRunnerConfig{CaptureConfig: compose.CaptureConfig{
		FreemailExtra:      []string{"gmail.com"},
		TransactionalExtra: []string{"sendgrid.net"},
	}})

	// Reached the resolver: the person exists and the identity is bound.
	_, personID := c.capturedMessage(t, u)
	if n := c.channelIdentities(t, u); n != 1 {
		t.Fatalf("%d channel identities for the captured sender, want 1 — the record never reached the resolver", n)
	}
	if n := c.count(t, `SELECT count(*) FROM person WHERE id = $1 AND owner_id IS NULL`, personID); n != 1 {
		t.Fatalf("the captured sender did not become one ownerless person")
	}

	// And no gate judged them. capture_pending_counterparty is the deferral
	// ledger; the system_log breadcrumbs are keyed on the record's source id,
	// so this counts the gates' verdicts about THIS message rather than
	// whatever else the installation logged.
	if n := c.count(t, `SELECT count(*) FROM capture_pending_counterparty`); n != 0 {
		t.Errorf("%d deferral rows for a channel record, want 0 — the ledger is address-keyed, so a verdict here is a verdict about nobody", n)
	}
	if n := c.count(t,
		`SELECT count(*) FROM system_log WHERE detail->>'source_id' = $1`, u.naturalKey()); n != 0 {
		t.Errorf("%d gate breadcrumbs for a channel record, want 0", n)
	}
}

// TestCaptureLatencyIsMeasuredOnTheAsyncPathNotTheWebhookAck holds AC7.1's
// 60-second window over the right pair of anchors.
//
// The ack is not the capture. Telegram's delivery is acknowledged the instant
// the raw update is durable, which is BEFORE any activity exists — so a
// latency measured at the ack reports success while the customer's message is
// still nowhere on the timeline. The window AC7.1 caps has to run from the
// receipt the webhook recorded to the capture the worker recorded, and this
// test pins each of the three instants involved so none can stand in for
// another:
//
//   - occurred_at is the customer's own send time, injected as the provider's
//     `date` and asserted exactly.
//   - raw_capture.received_at is the opening anchor, written by the webhook's
//     transaction.
//   - activity.created_at is the closing anchor, written by the worker's.
//
// The receipt anchor is moved to an INJECTED instant while the job is still
// queued — never with a sleep, and never read off the wall clock. That does two
// things at once. It reproduces a slow async leg, so the breach AC7.1 cares
// about becomes visible over this pair while the ack-anchored measurement is
// still reporting a capture that has not happened. And it makes the closing
// anchor falsifiable: if the worker stamped the activity from the raw row's
// receipt instead of from its own transaction, created_at would come back AS
// the injected instant, and the window would collapse to zero.
func TestCaptureLatencyIsMeasuredOnTheAsyncPathNotTheWebhookAck(t *testing.T) {
	c := setupTelegramConnected(t)
	u := telegramUpdate{updateID: 5501, messageID: 51, senderID: 770501, username: "waiting", firstName: "Sam", text: "how long?"}

	if status, _ := c.deliver(t, u); status != 200 {
		t.Fatal("the delivery was not accepted")
	}
	// AT THE ACK: durable, and not yet captured.
	if n := c.rawCaptures(t, u.updateID); n != 1 {
		t.Fatalf("%d raw captures at the ack, want 1 — Telegram has no history API, so the ack must not outrun durability", n)
	}
	if n := c.telegramActivities(t, u); n != 0 {
		t.Fatalf("%d activities already exist at the ack, want 0 — a latency closed here would be measuring the acknowledgement, not the capture", n)
	}

	injectedReceipt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE raw_capture SET received_at = $2
			  WHERE source_system = 'telegram' AND payload->>'update_id' = $1`,
			fmt.Sprintf("%d", u.updateID), injectedReceipt)
		return err
	}); err != nil {
		t.Fatalf("moving the receipt anchor to the injected instant: %v", err)
	}

	runner, sub := newTelegramWorker(t, c, compose.JobRunnerConfig{})
	startTelegramWorker(t, runner)
	awaitJobKind(t, sub, compose.TelegramIngestArgs{}.Kind())

	var receivedAt, createdAt, occurredAt time.Time
	if err := c.inWorkspace(t, c.slug, func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx,
			`SELECT received_at FROM raw_capture
			  WHERE source_system = 'telegram' AND payload->>'update_id' = $1`,
			fmt.Sprintf("%d", u.updateID)).Scan(&receivedAt); err != nil {
			return err
		}
		return tx.QueryRow(ctx,
			`SELECT created_at, occurred_at FROM activity WHERE source_system = 'telegram' AND source_id = $1`,
			u.naturalKey()).Scan(&createdAt, &occurredAt)
	}); err != nil {
		t.Fatalf("reading the capture's anchors: %v", err)
	}

	if !receivedAt.Equal(injectedReceipt) {
		t.Fatalf("received_at = %s, want the injected %s — the async leg rewrote the opening anchor, "+
			"so the window can never show how long a capture actually took", receivedAt, injectedReceipt)
	}
	if want := time.Unix(telegramProviderDate, 0).UTC(); !occurredAt.Equal(want) {
		t.Errorf("activity occurred_at = %s, want the injected provider send time %s — "+
			"the timeline instant is the customer's, and neither of this system's anchors", occurredAt, want)
	}
	if createdAt.Equal(receivedAt) {
		t.Fatalf("created_at and received_at are both %s — one instant serves as both anchors, "+
			"so the capture is being recorded as having happened at the ack", createdAt)
	}
	if window := createdAt.Sub(receivedAt); window <= captureLatencyBudget {
		t.Fatalf("the receipt→capture window measured %s against an injected receipt of %s; "+
			"a window that cannot exceed AC7.1's %s ceiling is not measuring the async leg at all",
			window, injectedReceipt, captureLatencyBudget)
	}
}
