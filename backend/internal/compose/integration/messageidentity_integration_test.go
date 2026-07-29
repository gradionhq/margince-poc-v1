// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The claim this whole path exists to make, proven end to end against a
// provider that behaves the way Gmail actually behaves: it discards the
// Message-ID it is handed and mints its own.
//
// Two things then have to hold, and neither is visible from either side alone.
// The provider's own copy of the message, captured minutes later under the
// identity it minted, must collapse ONTO the send's row rather than land beside
// it — otherwise every sent email appears twice. And the counterparty's reply,
// which roots its thread at the identity the world can see, must attribute to
// the SEND — the row that holds the delivery, the consent purpose and the
// links — rather than to a captured echo that holds none of them.
//
// Everything here is the production object but the provider: the send path, the
// dispatcher, the connector, the reconcile, mailmap and the capture sink.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/capture/mailmap"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// gmailStamped is the identity a real Gmail mailbox mints in place of the
// client's: a different namespace entirely, which is why nothing keyed on the
// minted one can ever meet the echo or the reply.
const gmailStamped = "CAFAR1txEuKW7Qh@mail.gmail.com"

// replyFrom the counterparty, threaded the only way a mail client can thread —
// by quoting the identity that arrived on the wire. It carries its own
// Message-ID because every message does; what matters is the ancestry.
func replyFrom(quoting string) []byte {
	return []byte("From: buyer@preflight.test\r\n" +
		"To: " + sendingMailbox + "\r\n" +
		"Subject: Re: Inbound question\r\n" +
		"Message-ID: <reply-1@buyer.preflight.test>\r\n" +
		"In-Reply-To: <" + quoting + ">\r\n" +
		"References: <" + quoting + ">\r\n" +
		"\r\n" +
		"That works for us.\r\n")
}

// activityKeyedOn resolves the one activity holding a natural key, and insists
// there is exactly one. The count IS the assertion in most of what follows: two
// rows on one message is the defect, and zero means the key never moved.
func (p *preflightEnv) activityKeyedOn(t *testing.T, sourceID string) ids.UUID {
	t.Helper()
	var found []ids.UUID
	if err := p.inWorkspace(t, p.slug, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`SELECT id FROM activity WHERE source_system = 'gmail' AND source_id = $1`, sourceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			found = append(found, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading the activities keyed on %q: %v", sourceID, err)
	}
	if len(found) != 1 {
		t.Fatalf("%d activities are keyed on %q, want exactly 1: %v", len(found), sourceID, found)
	}
	return found[0]
}

// captureEcho drives the real sink with the provider's own copy of a message,
// mapped exactly as the Gmail connector maps a message it re-reads.
func (p *preflightEnv) captureEcho(t *testing.T, stored []byte) {
	t.Helper()
	msg, err := mailmap.Parse(stored, sendingMailbox)
	if err != nil {
		t.Fatalf("the provider's stored copy does not parse:\n%s\n%v", stored, err)
	}
	if _, err := capture.NewSink(p.pool).Upsert(p.connectorCtx(t),
		msg.AttestSentByOwner(true).ToRecord("gmail", stored)); err != nil {
		t.Fatalf("capturing the provider's own copy: %v", err)
	}
}

// A provider that rewrote the identity: the send's row moves onto what the wire
// carries, the echo of that same message collapses onto it, and the reply the
// counterparty roots at the wire identity attributes to the send.
func TestAGmailRewrittenIdentityStillYieldsOneActivityAndOneReplyTarget(t *testing.T) {
	p := setupPreflight(t)
	p.connect(t, gmailReadonlyScope, gmailSendScope)

	sentActivity := p.sendExpectingAcceptance(t, "transactional", "Re: Inbound question", "As discussed.")
	deliveryID, mintedIdentity := p.deliveryFor(t, sentActivity)
	if mintedIdentity == gmailStamped {
		t.Fatalf("the send staged under %q, which is the identity the provider is meant to REPLACE — this case would prove nothing", mintedIdentity)
	}
	transmitted, _ := p.transmit(t, deliveryID, gmailStamped)

	// The reconcile ran inside RecordSent: the row the human reads is now keyed
	// on the identity the world can see, and the delivery agrees with it.
	if id := p.activityKeyedOn(t, gmailStamped); id != sentActivity {
		t.Fatalf("the stamped identity resolves to %s, want the send's own activity %s", id, sentActivity)
	}
	var deliveryMessageID string
	if err := p.inWorkspace(t, p.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT message_id FROM comms_outbound WHERE id = $1`, deliveryID).Scan(&deliveryMessageID)
	}); err != nil {
		t.Fatalf("reading the delivery's identity back: %v", err)
	}
	if deliveryMessageID != gmailStamped {
		t.Errorf("the delivery still reads %q, want %q — the send log and the timeline must name one message",
			deliveryMessageID, gmailStamped)
	}

	// THE COLLAPSE. The provider files its own copy back into the mailbox and
	// the sync re-reads it; the bytes are the ones the provider stored, and the
	// key comes out of them through the connector's own mapping.
	p.captureEcho(t, storedCopy(t, transmitted, gmailStamped))
	if id := p.activityKeyedOn(t, gmailStamped); id != sentActivity {
		t.Fatalf("after capturing the echo, the stamped identity resolves to %s, want the send's own activity %s — the send appears twice", id, sentActivity)
	}
	// And nothing is left behind under the identity the provider discarded.
	var stale int
	if err := p.inWorkspace(t, p.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM activity WHERE source_id = $1`, mintedIdentity).Scan(&stale)
	}); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Errorf("%d activities still carry the minted identity %q, which exists in no mailbox", stale, mintedIdentity)
	}

	// THE ATTRIBUTION. A reply can only quote what it received, so it roots at
	// the stamped identity; the matcher joins outbound activities on thread_key
	// and must land on the send.
	p.captureEcho(t, replyFrom(gmailStamped))
	var matched ids.UUID
	if err := p.inWorkspace(t, p.slug, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT (envelope->'payload'->>'matched_outbound_activity_id')::uuid
			  FROM event_outbox
			 WHERE envelope->>'type' = 'engagement.reply'`).Scan(&matched)
	}); err != nil {
		t.Fatalf("reading the reply match back: %v — the reply matched no outbound message at all", err)
	}
	if matched != sentActivity {
		t.Errorf("the reply attributes to %s, want the send %s — an echo carries none of the send's links, consent purpose or draft outcome",
			matched, sentActivity)
	}
}
