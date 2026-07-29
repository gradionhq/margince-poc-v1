// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// comms_outbound under the GDPR engines. The table stores the recipient
// addresses, the subject line and the body of every governed outbound
// message, so the three obligations that already reach the activity timeline
// have to reach it too: Art. 17 erasure, Art. 15 subject access, and the
// nightly retention evaluator. Each engine carries its own SQL — there is no
// registration list — so each is proven separately here.
//
// The statutory correspondence floor matters more here than anywhere else in
// this suite: a delivery hangs off an EMAIL activity, and an email is
// commercial correspondence under the jurisdiction pack
// retention_jurisdiction_integration_test.go arms for this whole binary (six
// calendar years, year-end anchored). Fixtures are aged past that floor on
// purpose, and the shielded case is asserted alongside the erased one — a
// delivery scrubbed while its activity is shielded would be a GoBD floor
// bypass through the back door, and one left behind while its activity is
// redacted would leave the whole message readable in the send log.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/comms"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// mailRecipientEmail is the erased subject's address — the one that must not
// survive in a delivery row the engines were supposed to reach. Named apart
// from erasure_integration_test.go's subjectEmail because the two suites seed
// different fixtures and a mixed-up constant would still compile.
const mailRecipientEmail = "erika.recipient@example.test"

// delivered is one seeded outbound message: the person it was sent to, the
// timeline row that records it, the delivery row that carried it, and the
// subject line and terminal status both held before any engine touched them.
type delivered struct {
	person   ids.UUID
	activity ids.UUID
	delivery ids.UUID
	subject  string
	status   string
}

// seedMailRecipient plants the data subject with one email channel — always
// mailRecipientEmail, because every assertion in this file reads that constant
// to decide whether the address survived a scrub.
func seedMailRecipient(t *testing.T, e *Env) ids.UUID {
	t.Helper()
	personID := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(ctx,
			`INSERT INTO person (id, workspace_id, full_name, source, captured_by)
			 VALUES ($1, `+wsClause+`, 'Erika Recipient', 'manual', 'human:x')`, personID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO person_email (workspace_id, person_id, email, source, captured_by)
			 VALUES (`+wsClause+`, $1, $2, 'manual', 'human:x')`, personID, mailRecipientEmail)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return personID
}

// seedDelivery plants an outbound email activity and the comms_outbound row
// that transmitted it, addressed to mailRecipientEmail. age is how long ago the
// message occurred, as a Postgres interval literal — the correspondence floor
// reads it, so it decides whether a destructive engine may touch the row at
// all. recipient is the address on the message; linkTo, when non-zero, links
// the activity to that person. The two are independent on purpose — a delivery
// addressed to a third party but linked to the subject's timeline, and one
// addressed to the subject with no link at all, are what tell the SAR's two
// reach arms apart.
//
// status decides which of the columns a scrub must PRESERVE are populated, and
// the two shapes are genuinely different rows: a sent delivery carries its
// receipt and no reason (RecordSent nulls it), while a parked one carries the
// operator sentence that ended it — which is where a provider's own words
// about the recipient land, and therefore the only place `reason` can hold the
// subject's address.
func seedDelivery(t *testing.T, e *Env, age, subject, body, status, recipient string, linkTo ids.UUID) delivered {
	t.Helper()
	return seedAddressedDelivery(t, e, age, subject, body, status,
		addresses{counterparty: recipient, to: recipient, cc: "cc." + recipient}, linkTo)
}

// addresses is one message's three address facts, which a real send does NOT
// keep in step: activities.SendEmail records only the FIRST addressee in the
// timeline row's counterparty_email, while the delivery keeps the whole To and
// Cc lists. Every seedDelivery fixture wants them agreeing; the erasure
// selector's Cc arm is the case where they must not.
type addresses struct{ counterparty, to, cc string }

// seedAddressedDelivery is seedDelivery with the address lists stated apart.
func seedAddressedDelivery(t *testing.T, e *Env, age, subject, body, status string, addr addresses, linkTo ids.UUID) delivered {
	t.Helper()
	out := delivered{person: linkTo, activity: ids.NewV7(), delivery: ids.NewV7(), subject: subject, status: status}
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, subject, body, direction, occurred_at,
			                      source, captured_by, source_system, source_id, counterparty_email)
			VALUES ($1, `+wsClause+`, 'email', $2, $3, 'outbound', now() - $4::interval,
			        'manual', 'human:x', 'gmail', $5, $6)`,
			out.activity, subject, body, age, out.delivery.String()+"@margince.test", addr.counterparty); err != nil {
			return err
		}
		if !linkTo.IsZero() {
			if _, err := tx.Exec(ctx,
				`INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
				 VALUES (`+wsClause+`, $1, 'person', $2)`, out.activity, linkTo); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO comms_outbound (id, workspace_id, activity_id, user_id, provider, message_id,
			                            recipients, cc, subject, body, consent_purpose,
			                            list_unsubscribe, status, sent_at, provider_message_id, reason)
			VALUES ($1, `+wsClause+`, $2, $3, 'gmail', $4,
			        jsonb_build_array($5::text), jsonb_build_array($9::text), $6, $7, 'transactional',
			        '<https://app.test/v1/public/preferences/tok-erika/unsubscribe?purpose=marketing>', $8,
			        CASE WHEN $8 = 'sent' THEN now() END,
			        CASE WHEN $8 = 'sent' THEN 'provider-receipt-' || $4 END,
			        CASE WHEN $8 = 'parked' THEN 'the provider refused the recipient ' || $5 END)`,
			out.delivery, out.activity, e.Rep1, out.delivery.String()+"@margince.test",
			addr.to, subject, body, status, addr.cc)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// deliveryRow is one comms_outbound row as the assertions read it: the columns
// a scrub must empty, and the ones it must leave alone.
type deliveryRow struct {
	recipients, cc, subject, body string
	listUnsubscribe, reason       *string
	status                        string
	sentAt                        *time.Time
	providerMessageID             *string
}

// readDelivery reads back one delivery, both halves.
func readDelivery(t *testing.T, e *Env, id ids.UUID) deliveryRow {
	t.Helper()
	var row deliveryRow
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT recipients::text, cc::text, subject, body, list_unsubscribe, reason,
			       status, sent_at, provider_message_id
			FROM comms_outbound WHERE id = $1`, id).
			Scan(&row.recipients, &row.cc, &row.subject, &row.body, &row.listUnsubscribe,
				&row.reason, &row.status, &row.sentAt, &row.providerMessageID)
	})
	if err != nil {
		t.Fatalf("reading delivery %s: %v", id, err)
	}
	return row
}

// assertDeliveryRedacted proves nothing of the message survives: no address in
// either recipient list, no body, and no unsubscribe token — which is a
// per-recipient identifier for the very subject whose data is supposed to be
// gone. The subject line is asserted against the ACTIVITY's, not against a
// literal: the two engines leave different tombstones, and what must hold in
// both is that the delivery reads exactly as the message's own timeline row.
func assertDeliveryRedacted(t *testing.T, e *Env, d delivered) {
	t.Helper()
	row := readDelivery(t, e, d.delivery)
	if strings.Contains(row.recipients, mailRecipientEmail) || strings.Contains(row.cc, mailRecipientEmail) {
		t.Errorf("the erased address still sits in the delivery: recipients=%s cc=%s", row.recipients, row.cc)
	}
	if row.body != "" {
		t.Errorf("delivery body survived the erasure: %q", row.body)
	}
	if row.listUnsubscribe != nil {
		t.Errorf("the recipient's unsubscribe token survived the erasure: %q", *row.listUnsubscribe)
	}
	if row.reason != nil {
		t.Errorf("the delivery reason survived the erasure: %q — it can quote the provider quoting the address", *row.reason)
	}
	// The other half of the posture, and the reason this is a scrub rather than
	// a delete: the proof that a message left — or was tried and stopped — must
	// outlive the loss of what it said, or erasure has destroyed the send record
	// instead of redacting it.
	if row.status != d.status {
		t.Errorf("the scrub moved the delivery's status from %q to %q", d.status, row.status)
	}
	if d.status == "sent" && (row.sentAt == nil || row.providerMessageID == nil) {
		t.Errorf("the scrub destroyed the receipt proving the message left: sent_at=%v provider_message_id=%v",
			row.sentAt, row.providerMessageID)
	}
	var activitySubject *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT subject FROM activity WHERE id = $1`, d.activity).Scan(&activitySubject)
	}); err != nil {
		t.Fatal(err)
	}
	if activitySubject == nil || *activitySubject == d.subject {
		t.Fatalf("fixture drift: the activity was not scrubbed (subject %v), so nothing pins the delivery", activitySubject)
	}
	if row.subject != *activitySubject {
		t.Errorf("delivery subject = %q but its activity reads %q — the two must carry one tombstone",
			row.subject, *activitySubject)
	}
}

// assertDeliveryIntact is the other half of assertDeliveryRedacted: a delivery
// no engine was entitled to touch, named by whatever keeps it. Every widening
// of what the cascade selects owes one, or a selector that matched everything
// would read here as a pass.
func assertDeliveryIntact(t *testing.T, e *Env, d delivered, what string) {
	t.Helper()
	if row := readDelivery(t, e, d.delivery); row.subject != d.subject || row.body == "" {
		t.Errorf("the scrub reached %s: subject=%q body=%q", what, row.subject, row.body)
	}
}

// Art. 17: erasing the person redacts the delivery behind every activity the
// cascade redacted — and only those. The floor-shielded sibling proves the
// scrub inherits the activity engine's shields rather than reaching by address
// on its own, which would destroy a Handelsbrief the nightly evaluator refuses
// to touch.
func TestErasureRedactsTheDeliveryBehindARedactedActivity(t *testing.T) {
	e := Setup(t)
	person := seedMailRecipient(t, e)
	// Both terminal shapes, because the scrub has to clear a different column
	// on each: a sent delivery's receipt must survive, a parked one's operator
	// reason quotes the address and must not.
	aged := seedDelivery(t, e, "9 years", "Old order confirmation", "the agreed price was 4200 EUR", "sent", mailRecipientEmail, person)
	agedParked := seedDelivery(t, e, "9 years", "Old quote", "the quote nobody could deliver", "parked", mailRecipientEmail, person)
	shielded := seedDelivery(t, e, "30 days", "Recent order confirmation", "the agreed price was 900 EUR", "sent", mailRecipientEmail, person)
	// The class the link-walk cannot see: a message this installation SENT to
	// the subject whose activity inherited no person link (its anchor had
	// none, or was linked to an organization or deal instead). Nothing links
	// it to the subject, and its captured_by is a human — so both halves of
	// the timeline selector miss it unless erasure reaches mail by address.
	unlinked := seedDelivery(t, e, "9 years", "Sent with no timeline link", "the unlinked quote", "sent", mailRecipientEmail, ids.UUID{})

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	assertDeliveryRedacted(t, e, aged)
	assertDeliveryRedacted(t, e, agedParked)
	assertDeliveryRedacted(t, e, unlinked)

	// The floor-shielded message keeps its activity, so its delivery keeps its
	// copy: the two must agree, always.
	var activityBody *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT body FROM activity WHERE id = $1`, shielded.activity).Scan(&activityBody)
	}); err != nil {
		t.Fatal(err)
	}
	if activityBody == nil {
		t.Fatal("fixture drift: the recent email must be shielded by the correspondence floor, but its activity was redacted")
	}
	if row := readDelivery(t, e, shielded.delivery); row.body == "" {
		t.Error("a delivery was scrubbed while its floor-shielded activity was not — the two must never disagree")
	}
}

// A send records only its FIRST addressee on the timeline row, so a subject who
// was a second recipient or a Cc is named nowhere the link-walk or the
// activity's own address column can see — while the delivery still holds their
// address, the message subject and its body. The erase has to reach them
// through the delivery's address lists or it leaves the whole message readable.
func TestErasureReachesAMessageWhereTheSubjectIsOnlyACcRecipient(t *testing.T) {
	e := Setup(t)
	person := seedMailRecipient(t, e)
	// Nothing on the timeline names the subject: no person link, and the
	// activity's counterparty_email is the To address. Only the delivery's cc
	// list records that this message reached them.
	ccd := seedAddressedDelivery(t, e, "9 years", "Quote for the renewal", "the quote we discussed", "sent",
		addresses{counterparty: "buyer@example.test", to: "buyer@example.test", cc: mailRecipientEmail}, ids.UUID{})
	// The control: the same shape and the same age, on a message the subject was
	// never on. A selector widened into matching every delivery would scrub it.
	unrelated := seedAddressedDelivery(t, e, "9 years", "Quote for another buyer", "an unrelated quote", "sent",
		addresses{counterparty: "other@example.test", to: "other@example.test", cc: "cc.other@example.test"}, ids.UUID{})
	// Both shields the link-walk arm owes are owed here too, and a widening that
	// bypassed either would be worse than the gap it closes. Same Cc-only shape,
	// one inside the statutory correspondence floor and one on a deal under
	// legal hold: each must survive the erase intact.
	fresh := seedAddressedDelivery(t, e, "30 days", "Recent renewal quote", "the quote from this month", "sent",
		addresses{counterparty: "buyer@example.test", to: "buyer@example.test", cc: mailRecipientEmail}, ids.UUID{})
	held := seedAddressedDelivery(t, e, "9 years", "Disputed renewal quote", "the quote before the dispute", "sent",
		addresses{counterparty: "buyer@example.test", to: "buyer@example.test", cc: mailRecipientEmail}, ids.UUID{})
	linkToHeldDeal(t, e, held.activity)

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	assertDeliveryRedacted(t, e, ccd)
	assertDeliveryIntact(t, e, unrelated, "a message the subject was never on")
	assertDeliveryIntact(t, e, fresh, "a message inside the statutory correspondence floor")
	assertDeliveryIntact(t, e, held, "a message on a deal under legal hold")
}

// A delivery still pending keeps a live River job, and the dispatcher transmits
// whatever the row holds — so a scrub that empties the content and leaves the
// status alone mails the tombstone it just wrote to the person who exercised
// Art. 17. Closing the row is what stops that; a message that already left
// keeps its status and its receipt, because rewriting those would falsify the
// send log.
func TestErasureParksAPendingDeliveryInsteadOfLeavingItToTransmit(t *testing.T) {
	e := Setup(t)
	person := seedMailRecipient(t, e)
	// Aged past the statutory correspondence floor, which decides whether the
	// erase may reach the activity at all: a fresh fixture would be shielded and
	// would prove nothing about the scrub.
	pending := seedDelivery(t, e, "9 years", "Queued for the subject",
		"the words still waiting to go out", "pending", mailRecipientEmail, person)
	sent := seedDelivery(t, e, "9 years", "Already gone", "the words that left", "sent", mailRecipientEmail, person)

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	row := readDelivery(t, e, pending.delivery)
	if row.status != "parked" {
		t.Errorf("a pending delivery for an erased person is still %q — its job would transmit to them", row.status)
	}
	switch {
	case row.reason == nil || *row.reason == "":
		t.Error("the closed delivery carries no reason; an operator cannot tell why it stopped")
	case strings.Contains(*row.reason, mailRecipientEmail):
		t.Errorf("the park reason names the erased address: %q", *row.reason)
	}
	if row.sentAt != nil {
		t.Errorf("a delivery that never left carries a send timestamp: %v", row.sentAt)
	}
	if strings.Contains(row.recipients, mailRecipientEmail) || strings.Contains(row.cc, mailRecipientEmail) || row.body != "" {
		t.Errorf("the closed delivery still holds the message: recipients=%s cc=%s body=%q",
			row.recipients, row.cc, row.body)
	}
	if sentRow := readDelivery(t, e, sent.delivery); sentRow.status != "sent" || sentRow.sentAt == nil {
		t.Errorf("the scrub rewrote a delivery that had already left: status=%q sent_at=%v",
			sentRow.status, sentRow.sentAt)
	}

	// The status is only as good as what the dispatcher makes of it: its load
	// is guarded on `pending`, so the parked row stops the redelivered job here
	// rather than reaching a provider.
	if _, err := comms.NewStore(e.Pool, nil).Load(e.Admin(), pending.delivery); !errors.Is(err, comms.ErrTerminal) {
		t.Errorf("the dispatcher's load of the closed delivery returned %v, want ErrTerminal", err)
	}
}

// linkToHeldDeal hangs an activity off a deal under legal_hold — the position
// a sent reply lands in by default, because the send path copies its anchor's
// organization and deal links onto the message it stages. No person link is
// written: this is the arm the link-walk cannot see.
func linkToHeldDeal(t *testing.T, e *Env, activityID ids.UUID) {
	t.Helper()
	pipeline, open, _ := DealFixture(t, e)
	dealID := e.SeedDeal(t, "Disputed renewal", pipeline, open, nil)
	e.WsExec(t, `UPDATE deal SET legal_hold = true WHERE id = $1`, dealID)
	e.WsExec(t,
		`INSERT INTO activity_link (workspace_id, activity_id, entity_type, deal_id)
		 VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, 'deal', $2)`,
		activityID, dealID)
}

// A litigation hold reaches mail the link-walk cannot see. Sent mail is
// STRUCTURALLY organization- and deal-linked — the send path inherits the
// anchor's links — and carries no person link of its own, so it is reached by
// address rather than by link. Destroying it because of that would spoliate
// litigation-held evidence the nightly retention evaluator refuses to touch,
// and the delivery behind it would go with it.
//
// The fixture is aged past the statutory correspondence floor on purpose: a
// recent email survives on the floor alone, which would keep this green with
// the hold exclusion removed.
func TestErasurePreservesUnlinkedMailUnderATransitiveLegalHold(t *testing.T) {
	e := Setup(t)
	person := seedMailRecipient(t, e)
	held := seedDelivery(t, e, "9 years", "Disputed renewal terms",
		"the terms we agreed before the dispute", "sent", mailRecipientEmail, ids.UUID{})
	linkToHeldDeal(t, e, held.activity)
	// The control: same shape, same age, no hold — so the survival above is
	// attributable to the legal_hold and not to the selector missing this class.
	free := seedDelivery(t, e, "9 years", "Ordinary quote",
		"the quote nobody disputed", "sent", mailRecipientEmail, ids.UUID{})

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	var subject string
	var body *string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT subject, body FROM activity WHERE id = $1`, held.activity).Scan(&subject, &body)
	}); err != nil {
		t.Fatal(err)
	}
	if subject != held.subject || body == nil || *body != "the terms we agreed before the dispute" {
		t.Errorf("held evidence was destroyed: subject=%q body=%v", subject, body)
	}
	// The delivery behind a held activity is evidence of the same message, so
	// the scrub must not reach it either.
	if row := readDelivery(t, e, held.delivery); row.body == "" || !strings.Contains(row.recipients, mailRecipientEmail) {
		t.Errorf("the delivery behind held evidence was scrubbed: recipients=%s body=%q", row.recipients, row.body)
	}
	assertDeliveryRedacted(t, e, free)
}

// Art. 15: the export owes the subject the messages sent to them, reached both
// through the timeline links and by their own address on the recipient list —
// a message the send path never linked to a record is still data about them.
func TestSARIncludesTheSubjectsSentMessages(t *testing.T) {
	e := Setup(t)
	person := seedMailRecipient(t, e)
	// One fixture per arm, and neither can satisfy the other: the linked
	// message went to somebody ELSE (only the timeline link ties it to the
	// subject), and the addressed one carries no link at all. Sharing an
	// address between them would let either clause alone keep this green.
	linked := seedDelivery(t, e, "10 days", "Linked to the record", "quoted terms",
		"sent", "third.party@example.test", person)
	unlinked := seedDelivery(t, e, "11 days", "Addressed but unlinked", "second quote",
		"sent", mailRecipientEmail, ids.UUID{})

	pkg, err := privacy.AssembleSAR(e.Admin(), e.Pool, ids.From[ids.PersonKind](person))
	if err != nil {
		t.Fatalf("AssembleSAR: %v", err)
	}

	found := map[string]bool{}
	for _, row := range pkg.SentMessages {
		subject, ok := row["subject"].(string)
		if !ok {
			t.Fatalf("sent message carries no subject string: %#v", row)
		}
		found[subject] = true
	}
	if len(pkg.SentMessages) != 2 {
		t.Fatalf("SAR carried %d sent messages, want both the linked and the addressed one: %#v",
			len(pkg.SentMessages), pkg.SentMessages)
	}
	if !found["Linked to the record"] {
		t.Errorf("the SAR missed the message linked to the subject's timeline (delivery %s)", linked.delivery)
	}
	if !found["Addressed but unlinked"] {
		t.Errorf("the SAR missed a message addressed to the subject with no timeline link (delivery %s)", unlinked.delivery)
	}
}

// The retention evaluator: a delivery ages out on exactly the schedule of the
// activity it belongs to. The activity's own erase action nulls its body, and
// the send log must not keep serving the same words afterwards.
func TestRetentionRedactsTheDeliveryOfAnAgedOutActivity(t *testing.T) {
	e := Setup(t)
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO retention_policy (workspace_id, object_type, category, retain_days, action)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'activity', NULL, 100, 'erase')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	aged := seedDelivery(t, e, "9 years", "Ancient campaign", "the words the policy ages out", "sent", mailRecipientEmail, ids.UUID{})
	fresh := seedDelivery(t, e, "10 days", "This week's message", "still within the window", "sent", mailRecipientEmail, ids.UUID{})

	svc := privacy.NewRetentionService(e.Pool, nil, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := svc.Evaluate(context.Background()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	assertDeliveryRedacted(t, e, aged)
	if row := readDelivery(t, e, fresh.delivery); row.body != "still within the window" {
		t.Errorf("an in-window delivery was aged out: body = %q", row.body)
	}
}
