// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The channel-identity binding: the one write path for
// person_channel_identity rows, and the one place the identity race between
// two simultaneous first messages is settled.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// ResolveOrCreateChannelIdentity binds ci to personID and returns the person
// the identity ACTUALLY belongs to once the dust settles — personID when this
// call created the binding, the incumbent's person when one already existed.
//
// It is an insert-then-adopt, not a read-then-insert, because the check and
// the act cannot be one statement: two first messages from the same Telegram
// user arrive concurrently, both lanes miss, and both callers would insert.
// Here the loser blocks on Postgres' speculative-insert lock until the winner
// commits, then reads the winner out and adopts it. The database is the
// arbiter, so there is no window to lose.
//
// A caller that had to CREATE a person before it could offer one must treat a
// returned id different from personID as having lost the race: its own person
// row is speculative and must not survive the transaction, or the human ends
// up on two records with the conversation on one of them.
//
// The row carries no audit entry of its own, by the same rule as person_email
// and person_phone: it is a satellite of the person write that encloses it,
// and that write's audit row is the one an auditor reads.
func ResolveOrCreateChannelIdentity(ctx context.Context, tx pgx.Tx, personID ids.PersonID, ci connector.ChannelIdentity) (ids.PersonID, error) {
	if ci.Provider == "" || ci.ChannelUserID == "" {
		return ids.PersonID{}, errors.New("people: a channel identity needs both a provider and a channel user id")
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return ids.PersonID{}, err
	}
	// source names the channel the binding came from; the provider IS that
	// channel, and unlike a mail message there is no per-record id worth
	// stamping — the binding outlives every message that refreshed it.
	tag, err := tx.Exec(ctx, `
		INSERT INTO person_channel_identity
			(workspace_id, person_id, provider, channel_user_id, username, source, captured_by)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $3, $6)
		ON CONFLICT (workspace_id, provider, channel_user_id) WHERE archived_at IS NULL
		DO NOTHING`,
		workspaceID(ctx), personID, ci.Provider, ci.ChannelUserID, ci.Username, by)
	if err != nil {
		return ids.PersonID{}, fmt.Errorf("people: binding channel identity: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return personID, nil
	}

	// Zero rows means a live binding already exists — either it predates this
	// call or the concurrent winner has now committed. A fresh statement takes
	// a fresh snapshot under READ COMMITTED, so this read sees it.
	var winner ids.PersonID
	err = tx.QueryRow(ctx, `
		SELECT person_id FROM person_channel_identity
		WHERE provider = $1 AND channel_user_id = $2 AND archived_at IS NULL`,
		ci.Provider, ci.ChannelUserID).Scan(&winner)
	if err != nil {
		return ids.PersonID{}, fmt.Errorf("people: reading the channel identity that won the bind: %w", err)
	}
	return winner, nil
}
