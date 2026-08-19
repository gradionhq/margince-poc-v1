// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// What the CRM sent as this member, remembered just long enough to tell one kind
// of echo from another.
//
// ZALO DELIVERS A MEMBER'S OWN OUTGOING MESSAGES BACK TO THEIR OWN SOCKET, as
// ordinary inbound frames carrying the same msgId the send returned. Two different
// things arrive that way and nothing in the frame separates them:
//
//   - a reply staged through the CRM, which the core already wrote as an activity.
//     Capturing that echo puts the rep's words on the customer's timeline twice.
//   - a reply the rep typed in the Zalo app on their PHONE, which nothing here has
//     ever seen. Dropping that echo leaves the customer's half of the conversation
//     on the timeline and the rep's half nowhere.
//
// The second is the COMMON case for the team this connector is for — the consent
// copy tells them to use their phone rather than Zalo Web — so neither "drop every
// echo" nor "keep every echo" is a defensible rule. Knowing which ids WE sent is
// the only thing that separates them, and this file is that knowledge.

import (
	"context"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// sentEntity is what the ledger would call this table, and sentTable is what SQL
// calls it — the same derivation as the other two tables', for the same reason.
//
// NOTHING RECORDS AGAINST IT, deliberately: a marker is bookkeeping about a
// message whose real record is the activity the core already wrote, and a ledger
// row per sent message would double the history of every reply to say that this
// connector remembered it. The name is derived here anyway so a later ledger row
// cannot invent a second spelling.
const sentEntity = "ext_zalo_personal_sent_message"

const sentTable = "ext." + sentEntity

// sentMarkerLife is how long a marker can still answer a question.
//
// It is bounded by the PROVIDER rather than chosen: an echo can only arrive while
// Zalo still holds the message, and a marker older than that window can never
// match anything a drain could read. The claimed retention is three days
// (`retention_time: 259201000`) and the one measurement anybody has taken saw a
// single message held for about an hour, so the window this is sized against is
// UNMEASURED (DESIGN §9.1) — which is exactly why the margin is generous rather
// than tight. A marker is a few dozen bytes and the failure a missing one causes
// is a duplicated reply on a customer's timeline; two weeks of them is cheaper
// than being wrong about the window once.
const sentMarkerLife = 14 * 24 * time.Hour

// rememberSent records that the CRM sent one message as this member.
//
// AFTER the transmission, and that ordering is not a preference: the id this keys
// on is the PROVIDER's, so it does not exist until the send has returned one.
// There is no "record first" version of this to choose against.
//
// A send that returned no id records nothing, which is the honest outcome — there
// is no key to suppress on, so that one echo will be captured as a phone reply and
// the timeline will carry the reply twice. Inventing a key would be worse: it
// would suppress a real message that happened to be read at the same moment.
func rememberSent(ctx context.Context, rt extension.Runtime, member extension.UserID, messageID string) error {
	if messageID == "" {
		return nil
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO `+sentTable+` (workspace_id, user_id, provider_message_id)
			 VALUES (`+callerWorkspace+`, $1::uuid, $2)
			 ON CONFLICT (workspace_id, user_id, provider_message_id) DO NOTHING`,
			string(member), messageID)
		return err
	})
}

// sentByThisCRM answers which of a drain's echoed ids the CRM sent itself.
//
// ONE statement for the whole drain rather than one per frame: a tick can read a
// member's entire undelivered backlog, and a round trip per message to answer a
// bookkeeping question would cost more than the capture it guards.
//
// It runs in a transaction of its own, which is CLOSED before anything is
// ingested — the same rule the rest of the tick keeps.
func sentByThisCRM(ctx context.Context, rt extension.Runtime, member string, ids []string) (map[string]bool, error) {
	found := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return found, nil
	}
	err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT provider_message_id FROM `+sentTable+`
			  WHERE user_id = $1::uuid AND provider_message_id = ANY($2::text[])`, member, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			found[id] = true
		}
		return rows.Err()
	})
	return found, err
}

// forgetSentMarkersOf drops every marker of one member, because the account they
// name is no longer the account this connection is for.
//
// It is called from the ONE place that already answers the same question about the
// cursor — a member connecting a DIFFERENT Zalo account — so what connecting a
// different account invalidates is stated once rather than spread across two
// tables' worth of reasoning. Without it, an id minted by the old account could
// suppress a real message in the new one.
func forgetSentMarkersOf(ctx context.Context, tx extension.Tx, member string) error {
	_, err := tx.Exec(ctx, `DELETE FROM `+sentTable+` WHERE user_id = $1::uuid`, member)
	return err
}

// forgetOldSentMarkers drops markers no echo can still arrive for.
//
// It runs on the tick rather than on a schedule of its own, because the only thing
// that makes a marker worth keeping is a drain that might still read the message
// it names — and the drain is there. Without it this table is the one thing in the
// unit that grows forever: a row per reply, kept to answer a question that expired
// days ago.
func forgetOldSentMarkers(ctx context.Context, rt extension.Runtime) error {
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM `+sentTable+` WHERE created_at < now() - $1::interval`,
			sentMarkerLife.String())
		return err
	})
}
