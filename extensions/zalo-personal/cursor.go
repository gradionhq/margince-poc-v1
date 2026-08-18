// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// HOW FAR EACH CONVERSATION HAS ALREADY BEEN READ — one bookmark per counterparty,
// in a table of its own.
//
// THIS HOME WAS ARRIVED AT BY GETTING IT WRONG TWICE, and both wrong answers are
// worth keeping in view because each looks reasonable until a case exposes it:
//
//   1. ONE BOOKMARK PER MEMBER is a maximum over every conversation, so a message
//      landing from an included conversation buries every lower-numbered message
//      dropped from an excluded one. The member then includes that conversation and
//      it starts EMPTY.
//   2. A COLUMN ON THE VERDICT ROW fixes that and ties the bookmark's existence to a
//      row the MEMBER owns. It survived only as long as the allowlist was the only
//      model: under all_but_blocked most conversations have no verdict at all, so
//      there is nowhere to put their bookmark.
//
// The invariant both misses is that a bookmark is a fact about ONE CONVERSATION and
// about WHAT THIS INSTALLATION HAS DONE. A verdict is a fact about what a MEMBER
// DECIDED. They are written by different actors, read for different questions, and
// have different retention — a verdict is consent and must survive, a bookmark is
// scheduling state whose loss costs one deduplicated replay. Storing one inside the
// other is what made a mode change break a cursor.
//
// ABSENCE IS THE LOAD-BEARING STATE. A conversation with no row here has no bookmark,
// so everything Zalo is still holding for it passes — which is how a newly included
// conversation gets its queued messages instead of starting empty.

import (
	"context"
	"time"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// bookmark is one conversation's reading position AND WHEN THAT POSITION WAS
// WRITTEN, which is two facts because the second is what makes the first readable.
//
// A position alone cannot say which of the member's answers it is evidence of. Under
// everyone_except the mode carries a floor, and the presence of a bookmark is what
// admitsUnderMode reads as "capture has been reading this conversation, so the floor
// is not the question here". A position minted under a PREVIOUS answer supports no
// such reading: it says where that answer got to. Without the timestamp the two are
// indistinguishable, and a mode round-trip re-stamps the floor and then never
// consults it.
type bookmark struct {
	// at is the highest provider message id already ingested for this conversation.
	// Empty is the ABSENCE of a bookmark, which is the state that lets everything
	// Zalo is still holding for a newly included conversation through.
	at string
	// written is when this position was last advanced — the row's own updated_at,
	// which the tick is the only writer of.
	written time.Time
}

// postdates reports that this bookmark was written after the given floor, and is
// therefore evidence about the consent regime that floor belongs to.
//
// A bookmark with no row behind it has a zero time and postdates nothing, which is
// the correct answer: there is no bookmark to be evidence of anything.
func (b bookmark) postdates(floor time.Time) bool {
	return b.written.After(floor)
}

// cursorEntity is what the ledger would call this table, and cursorTable is what SQL
// calls it — the same derivation as the unit's other tables', for the same reason.
//
// NOTHING RECORDS AGAINST IT, deliberately: a reading position is bookkeeping about
// messages whose real record is the activity capture already wrote, and a ledger row
// per bookmark would double the history of every captured message to say that this
// connector remembered where it got to.
const cursorEntity = "ext_zalo_personal_conversation_cursor"

const cursorTable = "ext." + cursorEntity

// cursorsOf reads every conversation's reading position for one member.
//
// The whole set at once rather than a lookup per message: one drain can touch every
// conversation a member has, and a query per frame would be a round trip per message
// on the one path that must hold nothing open while it ingests.
func cursorsOf(ctx context.Context, tx extension.Tx, member string) (map[string]bookmark, error) {
	rows, err := tx.Query(ctx,
		`SELECT channel_user_id, last_msg_id, updated_at FROM `+cursorTable+`
		  WHERE user_id = $1::uuid`, member)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]bookmark{}
	for rows.Next() {
		var (
			counterparty string
			mark         bookmark
		)
		if err := rows.Scan(&counterparty, &mark.at, &mark.written); err != nil {
			return nil, err
		}
		found[counterparty] = mark
	}
	return found, rows.Err()
}

// advanceCursors moves each conversation's bookmark to what this turn actually
// landed, in ONE statement and in a transaction of its own — opened after every
// ingest has returned, which is the rule the tick is shaped around.
//
// GREATEST() rather than a bare assignment, so a bookmark cannot go BACKWARDS if two
// ticks overlap: the older one would otherwise re-offer the newer one's messages. The
// comparison is numeric because these are decimal ids whose text order is not their
// value order, and the column's CHECK is what guarantees every stored value parses.
func advanceCursors(ctx context.Context, rt extension.Runtime, member string,
	reached map[string]string,
) error {
	if len(reached) == 0 {
		return nil
	}
	counterparties, positions := make([]string, 0, len(reached)), make([]string, 0, len(reached))
	for counterparty, at := range reached {
		counterparties, positions = append(counterparties, counterparty), append(positions, at)
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO `+cursorTable+` (workspace_id, user_id, channel_user_id, last_msg_id)
			 SELECT `+callerWorkspace+`, $1::uuid, reached.counterparty, reached.at
			   FROM unnest($2::text[], $3::text[]) AS reached(counterparty, at)
			 ON CONFLICT (workspace_id, user_id, channel_user_id) DO UPDATE
			    SET last_msg_id = greatest(`+cursorTable+`.last_msg_id::numeric,
			                               EXCLUDED.last_msg_id::numeric)::text,
			        updated_at = now()`,
			member, counterparties, positions)
		return err
	})
}

// forgetCursorsOf drops every reading position this member holds, because the account
// whose message ids they name is no longer the account this connection is for.
//
// It is called from the ONE place that already answers the same question about the
// send markers — a member connecting a DIFFERENT Zalo account — so what connecting a
// different account invalidates is stated once. Without it, a position minted in the
// old account's id space would silently filter the new account's first messages as
// already-landed.
func forgetCursorsOf(ctx context.Context, tx extension.Tx, member string) error {
	_, err := tx.Exec(ctx, `DELETE FROM `+cursorTable+` WHERE user_id = $1::uuid`, member)
	return err
}
