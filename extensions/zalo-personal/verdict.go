// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The verdict table: what a member has decided about each of their own
// counterparties, and how far capture has got with each one.
//
// SPLIT FROM allowlist.go, which owns the two operations a member drives from their
// screen, because this is the STORE and that is the surface. The rule most worth
// reading is on last_msg_id: the cursor is per counterparty, and the reason is that a
// single per-member cursor is a maximum over every conversation — so a message
// landing from an allowed counterparty buries every lower-numbered message dropped
// from a conversation the member had not yet chosen, and that conversation then
// starts empty the moment they choose it. Two independent reviews found that defect;
// per-counterparty is the shape that cannot have it.

import (
	"context"
	"errors"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

// allowlistEntity is what the LEDGER calls the verdict table, and allowlistTable
// is what SQL calls it — the same derivation as the connection table's, for the
// same reason: audit_log.entity_type names a kind of record and takes no schema,
// while a statement resolves through a search_path the ext schema is not on.
const allowlistEntity = "ext_zalo_personal_allowlist"

const allowlistTable = "ext." + allowlistEntity

// allowlistColumns is the projection every read and every write of a verdict
// returns, in one place so a column added to the table is one edit.
const allowlistColumns = `id::text, channel_user_id, mode, coalesce(display_name, ''),
	coalesce(last_msg_id, ''), version`

// allowEntry is one stored verdict.
type allowEntry struct {
	ID            string
	ChannelUserID string
	Mode          verdict
	DisplayName   string
	// LastMsgID is the highest provider message id already ingested FOR THIS
	// COUNTERPARTY. It is the tick's bookmark and no operation on this surface
	// writes it: a member choosing a conversation states a verdict, not a position
	// in it, and a newly allowed counterparty having no cursor is what lets the
	// messages Zalo is still holding for them through.
	LastMsgID string
	Version   int
}

// scanAllowEntry reads allowlistColumns off one row. The mode is scanned as text
// and narrowed here: the column's CHECK is what guarantees the value, and a
// scan straight into the named type would make an unexpected string a silent
// verdict nothing in Go declared.
func scanAllowEntry(scan func(...any) error) (allowEntry, error) {
	var (
		entry allowEntry
		mode  string
	)
	if err := scan(&entry.ID, &entry.ChannelUserID, &mode, &entry.DisplayName,
		&entry.LastMsgID, &entry.Version); err != nil {
		return allowEntry{}, err
	}
	entry.Mode = verdict(mode)
	return entry, nil
}

// verdictsOf reads every verdict this member has recorded.
//
// The whole list at once rather than a lookup per message: a member's list is
// what one tick filters a whole drain against, and a query per frame would be
// one round trip per message on a path that already holds no transaction open
// while it ingests.
func verdictsOf(ctx context.Context, tx extension.Tx, member string) ([]allowEntry, error) {
	rows, err := tx.Query(ctx,
		`SELECT `+allowlistColumns+` FROM `+allowlistTable+`
		  WHERE user_id = $1::uuid ORDER BY channel_user_id`, member)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []allowEntry
	for rows.Next() {
		entry, err := scanAllowEntry(rows.Scan)
		if err != nil {
			return nil, err
		}
		found = append(found, entry)
	}
	return found, rows.Err()
}

// verdictsByCounterparty is the shape the filter reads: account id to decision.
// A counterparty absent from it is verdictNone, which admits(...) treats exactly
// as a block — the zero value of the map lookup being the deny answer is what
// makes default-deny structural rather than a branch somebody can invert.
func verdictsByCounterparty(entries []allowEntry) map[string]verdict {
	byID := make(map[string]verdict, len(entries))
	for _, entry := range entries {
		byID[entry.ChannelUserID] = entry.Mode
	}
	return byID
}

// cursorsByCounterparty is how far capture has got with each conversation. A
// counterparty with no bookmark is absent rather than present-and-empty, which is the
// same thing to atOrBelow and reads correctly at every call site: nothing has landed
// for them yet.
func cursorsByCounterparty(entries []allowEntry) map[string]string {
	cursors := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.LastMsgID != "" {
			cursors[entry.ChannelUserID] = entry.LastMsgID
		}
	}
	return cursors
}

// writeVerdicts upserts every entry and records each one.
//
// ONE LEDGER ROW PER VERDICT, deliberately: which counterparties a member
// allowed is the record of their consent, and a summary saying "17 entries
// saved" cannot answer the question somebody will actually ask, which is whether
// this installation was ever permitted to read a named conversation.
func writeVerdicts(ctx context.Context, tx extension.Tx, member string, entries []savedEntry) error {
	for _, entry := range entries {
		before, err := verdictFor(ctx, tx, member, entry.ChannelUserID)
		if err != nil {
			return err
		}
		after, err := scanAllowEntry(tx.QueryRow(ctx,
			`INSERT INTO `+allowlistTable+`
			        (workspace_id, user_id, channel_user_id, mode, display_name)
			 VALUES (`+callerWorkspace+`, $1::uuid, $2, $3, NULLIF($4, ''))
			 ON CONFLICT (workspace_id, user_id, channel_user_id) DO UPDATE
			    SET mode = EXCLUDED.mode,
			        display_name = coalesce(EXCLUDED.display_name, `+allowlistTable+`.display_name),
			        -- THE CURSOR IS NOT IN THIS SET LIST, and leaving it out is the
			        -- whole of what makes a re-allow work: a counterparty allowed
			        -- for the first time inserts with no cursor, so everything Zalo
			        -- still holds for them passes on the next tick. One that was
			        -- allowed, blocked and allowed again keeps the position it
			        -- genuinely reached, and the messages during the block were
			        -- correctly never captured. Resetting it here would re-offer a
			        -- whole conversation every time somebody edited their list.
			        version = `+allowlistTable+`.version + 1,
			        updated_at = now()
			 RETURNING `+allowlistColumns,
			member, entry.ChannelUserID, entry.Mode, entry.DisplayName).Scan)
		if err != nil {
			return err
		}
		if err := recordVerdict(ctx, tx, before, &after); err != nil {
			return err
		}
	}
	return nil
}

// verdictFor reads one stored verdict, or nothing. It is the before-image of the
// upsert above: what is recorded is the row the database held rather than what
// this code believed was there.
func verdictFor(ctx context.Context, tx extension.Tx, member, counterparty string) (*allowEntry, error) {
	found, err := scanAllowEntry(tx.QueryRow(ctx,
		`SELECT `+allowlistColumns+` FROM `+allowlistTable+`
		  WHERE user_id = $1::uuid AND channel_user_id = $2`, member, counterparty).Scan)
	if err != nil {
		if errors.Is(err, extension.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &found, nil
}

// countAllowed is how many conversations are armed, for the member's own status
// screen. It counts the rows the FILTER would admit rather than every row in the
// list, because "3 of your conversations are being captured" is the sentence the
// screen has to be able to make true.
func countAllowed(ctx context.Context, tx extension.Tx, member string) (int, error) {
	var allowed int
	err := tx.QueryRow(ctx,
		`SELECT count(*)::int FROM `+allowlistTable+`
		  WHERE user_id = $1::uuid AND mode = $2`, member, string(verdictAllow)).Scan(&allowed)
	if err != nil {
		return 0, err
	}
	return allowed, nil
}

// advanceVerdictCursors moves each counterparty's bookmark to what this turn
// actually landed, in ONE statement and in a transaction of its own — opened after
// every ingest has returned, which is the rule the tick is shaped around.
//
// ONE STATEMENT FOR THE WHOLE TURN rather than one per counterparty: a drain can
// touch every conversation a member has, and a round trip each would put the cursor
// write on the same footing as the ingest it is bookkeeping for.
//
// It is NOT version-guarded, deliberately. A verdict that changed under the turn is
// exactly the case where the mode matters and the position does not: if the member
// blocked that counterparty, the cursor names messages that were already captured
// before they blocked, and refusing to record it would re-offer them if the
// conversation is ever allowed again. What must not happen on a changed verdict is
// an INGEST, and that is guarded where it belongs — in the tick, by re-reading the
// verdicts after the drain.
//
// GREATEST() rather than a bare assignment, so a cursor cannot go BACKWARDS if two
// ticks overlap: the older one would otherwise re-offer the newer one's messages.
func advanceVerdictCursors(ctx context.Context, rt extension.Runtime, member string,
	reached map[string]string,
) error {
	if len(reached) == 0 {
		return nil
	}
	counterparties, cursors := make([]string, 0, len(reached)), make([]string, 0, len(reached))
	for counterparty, cursor := range reached {
		counterparties, cursors = append(counterparties, counterparty), append(cursors, cursor)
	}
	return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE `+allowlistTable+` AS entry
			    SET last_msg_id = greatest(coalesce(entry.last_msg_id, '0')::numeric,
			                               reached.cursor::numeric)::text,
			        updated_at = now()
			   FROM unnest($2::text[], $3::text[]) AS reached(counterparty, cursor)
			  WHERE entry.user_id = $1::uuid AND entry.channel_user_id = reached.counterparty`,
			member, counterparties, cursors)
		return err
	})
}

// forgetVerdictCursorsOf clears every bookmark this member holds, because the
// account whose message ids they name is no longer the account this connection is
// for. The verdicts themselves are kept — see the call site in connection.go.
func forgetVerdictCursorsOf(ctx context.Context, tx extension.Tx, member string) error {
	_, err := tx.Exec(ctx,
		`UPDATE `+allowlistTable+` SET last_msg_id = NULL, updated_at = now()
		  WHERE user_id = $1::uuid AND last_msg_id IS NOT NULL`, member)
	return err
}
