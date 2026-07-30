// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// A CHANNEL-shaped comms_outbound row under Art. 17 erasure. It is proven apart
// from the mail arm (privacy_comms_integration_test.go) because the row is a
// different shape and the scrub is a different statement: comms_outbound admits a
// mail-shaped row or a channel-shaped one and never half of each
// (comms_outbound_shape, 0149), so writing mail's empty address lists onto a
// channel row is not merely meaningless — the constraint REFUSES it, and an
// erasure that reached a channel delivery would fail outright rather than
// silently under-scrub.
//
// What must survive is the same as on the mail arm: the proof a message left.
// What must not is the subject's own account id — the Telegram user id, which is
// the channel's exact analogue of an address, and the value person_channel_identity
// holds for the same human.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// channelSubjectAccount is the erased subject's Telegram account id — the value
// that must not survive in a delivery row.
const channelSubjectAccount = "778899001"

// channelDelivery is one seeded outbound channel message and the rows behind it.
type channelDelivery struct {
	person   ids.UUID
	activity ids.UUID
	delivery ids.UUID
}

// seedChannelSubject plants a Telegram-only data subject: a person with a channel
// identity and no address at all, which is exactly the subject the address-shaped
// engines could never reach.
func seedChannelSubject(t *testing.T, e *Env) ids.UUID {
	t.Helper()
	personID := ids.NewV7()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(ctx,
			`INSERT INTO person (id, workspace_id, full_name, source, captured_by)
			 VALUES ($1, `+wsClause+`, 'Tilda Telegram', 'connector:telegram', 'connector:telegram')`,
			personID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO person_channel_identity
			  (workspace_id, person_id, provider, channel_user_id, username, source, captured_by)
			VALUES (`+wsClause+`, $1, 'telegram', $2, 'tilda', 'connector:telegram', 'connector:telegram')`,
			personID, channelSubjectAccount)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return personID
}

// seedChannelDelivery plants an outbound Telegram activity, its person link, and
// the channel-shaped delivery row that carried it. status is the delivery's
// terminal state, or 'pending' for the one the scrub must close so an erased
// subject cannot be messaged by a delivery that outlived them.
func seedChannelDelivery(t *testing.T, e *Env, age, body, status string, person ids.UUID) channelDelivery {
	t.Helper()
	out := channelDelivery{person: person, activity: ids.NewV7(), delivery: ids.NewV7()}
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		ctx := context.Background()
		wsClause := `NULLIF(current_setting('app.workspace_id', true), '')::uuid`
		if _, err := tx.Exec(ctx, `
			INSERT INTO activity (id, workspace_id, kind, body, direction, occurred_at,
			                      source, captured_by, source_system, source_id)
			VALUES ($1, `+wsClause+`, 'telegram', $2, 'outbound', now() - $3::interval,
			        'connector:telegram', 'human:x', 'telegram', $4)`,
			out.activity, body, age, out.activity.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
			 VALUES (`+wsClause+`, $1, 'person', $2)`, out.activity, person); err != nil {
			return err
		}
		// cc and references_chain are named as NULL for the reason
		// comms.Store.StageChannelTx names them: both still carry the mail shape's
		// DEFAULT of an empty JSON array, and a channel row that inherited it
		// would be refused by comms_outbound_shape.
		_, err := tx.Exec(ctx, `
			INSERT INTO comms_outbound (id, workspace_id, activity_id, user_id, provider,
			                            channel_user_id, body, consent_purpose, status,
			                            cc, references_chain,
			                            sent_at, provider_message_id)
			VALUES ($1, `+wsClause+`, $2, $3, 'telegram', $4, $5, 'transactional', $6,
			        NULL, NULL,
			        CASE WHEN $6 = 'sent' THEN now() END,
			        CASE WHEN $6 = 'sent' THEN $7 END)`,
			out.delivery, out.activity, e.Rep1, channelSubjectAccount, body, status,
			"tg-receipt-"+out.delivery.String())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// channelDeliveryRow is what the assertions read back.
type channelDeliveryRow struct {
	channelUserID     *string
	body              string
	status            string
	reason            *string
	providerMessageID *string
}

func readChannelDelivery(t *testing.T, e *Env, id ids.UUID) channelDeliveryRow {
	t.Helper()
	var row channelDeliveryRow
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT channel_user_id, body, status, reason, provider_message_id
			FROM comms_outbound WHERE id = $1`, id).
			Scan(&row.channelUserID, &row.body, &row.status, &row.reason, &row.providerMessageID)
	})
	if err != nil {
		t.Fatalf("reading channel delivery %s: %v", id, err)
	}
	return row
}

func TestErasureRedactsAChannelDeliveryWithoutBreakingItsShape(t *testing.T) {
	e := Setup(t)
	person := seedChannelSubject(t, e)
	sent := seedChannelDelivery(t, e, "9 years", "the agreed price was 4200 EUR", "sent", person)
	pending := seedChannelDelivery(t, e, "9 years", "still queued when they asked to be forgotten", "pending", person)

	if err := privacy.NewEraser(e.Pool).ErasePerson(e.Admin(), person, "test"); err != nil {
		t.Fatalf("ErasePerson: %v", err)
	}

	for name, d := range map[string]channelDelivery{"sent": sent, "pending": pending} {
		row := readChannelDelivery(t, e, d.delivery)
		// The account id is EMPTIED, not nulled: it is also the row's shape
		// discriminator, so nulling it would re-declare a channel delivery as
		// mail with every mail column missing — which the constraint refuses.
		if row.channelUserID == nil {
			t.Errorf("%s: channel_user_id was nulled; the row no longer declares its own shape", name)
		} else if *row.channelUserID != "" {
			t.Errorf("%s: the subject's account id survived the erasure as %q", name, *row.channelUserID)
		}
		if row.body != "" {
			t.Errorf("%s: the message body survived the erasure: %q", name, row.body)
		}
	}

	// A message that went out did go out: the receipt is the record of what
	// became of it, and a scrub that rewrote that would falsify the send log.
	if got := readChannelDelivery(t, e, sent.delivery); got.status != "sent" || got.providerMessageID == nil {
		t.Errorf("a sent channel delivery lost its receipt: status=%q receipt=%v", got.status, got.providerMessageID)
	}

	// A delivery still pending has a live job behind it, so leaving its status
	// alone would transmit the tombstone to the very subject who asked to be
	// forgotten. Parked, it is terminal to the dispatcher.
	parked := readChannelDelivery(t, e, pending.delivery)
	if parked.status != "parked" {
		t.Errorf("a pending channel delivery was left pending after an erasure: status=%q", parked.status)
	}
	if parked.reason == nil || *parked.reason == "" {
		t.Error("a channel delivery parked by the scrub carries no reason an operator can read")
	}

	// The rows still satisfy comms_outbound_shape — proven by the database
	// accepting one more write to each, which a violated CHECK would refuse.
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE comms_outbound SET attempts = attempts WHERE id = ANY($1)`,
			[]ids.UUID{sent.delivery, pending.delivery})
		return err
	}); err != nil {
		t.Fatalf("a scrubbed channel delivery no longer satisfies the shape constraint: %v", err)
	}
}
