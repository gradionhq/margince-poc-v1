// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The verdict table: what a member has decided about each of their own
// counterparties.
//
// SPLIT FROM allowlist.go, which owns the two operations a member drives from their
// screen, because this is the STORE and that is the surface.
//
// TWO MODES, ONE TABLE: the `block` rows are the exclusion list read under
// all_but_blocked, and the `allow` rows are the inclusion list read under
// only_allowed. A row is inert in the other mode rather than wrong in it, which is
// why switching modes rewrites nothing here.
//
// NO READING POSITION LIVES HERE. One used to be a column on these rows, and the
// two-mode model is what exposed it as a defect: under all_but_blocked most
// conversations have no verdict at all, so a bookmark on the verdict row has nowhere
// to go. Positions live in cursor.go, whose header carries the full argument.

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
const allowlistColumns = `id::text, channel_user_id, mode, coalesce(display_name, ''), version`

// allowEntry is one stored verdict.
type allowEntry struct {
	ID            string
	ChannelUserID string
	Mode          verdict
	DisplayName   string
	Version       int
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
	if err := scan(&entry.ID, &entry.ChannelUserID, &mode, &entry.DisplayName, &entry.Version); err != nil {
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

// writeVerdicts upserts every entry and records each one.
//
// ONE LEDGER ROW PER VERDICT, deliberately: which counterparties a member
// allowed is the record of their consent, and a summary saying "17 entries
// saved" cannot answer the question somebody will actually ask, which is whether
// this installation was ever permitted to read a named conversation.
//
// An entry whose verdict is `none` REMOVES the person from the list, which is what a
// search-as-you-type picker does when somebody is taken out of it.
func writeVerdicts(ctx context.Context, tx extension.Tx, member string, entries []savedEntry) error {
	for _, entry := range entries {
		before, err := verdictFor(ctx, tx, member, entry.ChannelUserID)
		if err != nil {
			return err
		}
		if entry.Mode == string(verdictNone) {
			if err := dropVerdict(ctx, tx, member, before); err != nil {
				return err
			}
			continue
		}
		after, err := scanAllowEntry(tx.QueryRow(ctx,
			`INSERT INTO `+allowlistTable+`
			        (workspace_id, user_id, channel_user_id, mode, display_name)
			 VALUES (`+callerWorkspace+`, $1::uuid, $2, $3, NULLIF($4, ''))
			 ON CONFLICT (workspace_id, user_id, channel_user_id) DO UPDATE
			    SET mode = EXCLUDED.mode,
			        display_name = coalesce(EXCLUDED.display_name, `+allowlistTable+`.display_name),
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
		// A `block` that becomes an `allow` is an exclusion LIFTED, and the row that
		// recorded it has just been overwritten — so unless the instant of the lift is
		// written here, in this same transaction, nothing is left to say the excluded
		// period existed and the whole of it lands on the next tick. It is the same
		// act dropVerdict handles for the removal path, and both route through
		// exclusionLifted so there is ONE answer to what a lift is.
		if !exclusionLifted(before, after.Mode) {
			continue
		}
		if err := raiseFloor(ctx, tx, member, entry.ChannelUserID); err != nil {
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
