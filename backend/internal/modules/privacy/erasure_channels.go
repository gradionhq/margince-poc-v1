// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The channel half of Art. 17 erasure. A person's channel identities — the
// provider account id behind their messages and the @username it carries —
// identify the subject as directly as an address does, and the id is the key a
// re-capture would resurrect them by. So the same three steps the address half
// runs apply here, in the same order and for the same reason: collect the
// identifiers, delete the rows, then arm the suppression list, because once the
// rows are gone nothing holds the identifiers to hash.
//
// It lives beside erasure.go rather than inside it so the cascade file stays
// under the file-length cap; erasureCascadeFiles in the PII-coverage gate lists
// both, so extracting a scrub here does not take it out of that gate's sight.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// channelIdentity is one (provider, channel_user_id) pair — the suppression key
// the eraser has to hash before it deletes the row holding it.
type channelIdentity struct {
	Provider      string
	ChannelUserID string
}

// eraseChannelIdentities removes the subject's channel identities and suppresses
// them, returning how many were suppressed for the erasure tombstone's counts.
// Runs inside the caller's single erasure transaction: a delete that committed
// without its suppression row would leave the subject erasable-but-resurrectable,
// which is indistinguishable from a working erasure until the next message
// arrives.
func eraseChannelIdentities(ctx context.Context, tx pgx.Tx, personID ids.PersonID) (int, error) {
	rows, err := tx.Query(ctx,
		`SELECT provider, channel_user_id FROM person_channel_identity WHERE person_id = $1`, personID)
	if err != nil {
		return 0, err
	}
	identities, err := pgx.CollectRows(rows, pgx.RowToStructByPos[channelIdentity])
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM person_channel_identity WHERE person_id = $1`, personID); err != nil {
		return 0, err
	}
	for _, identity := range identities {
		if _, err := tx.Exec(ctx, `
			INSERT INTO erasure_suppression (workspace_id, kind, value_hash)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, 'channel_identity', $1)
			ON CONFLICT DO NOTHING`,
			storekit.ChannelIdentityHash(identity.Provider, identity.ChannelUserID)); err != nil {
			return 0, err
		}
	}
	return len(identities), nil
}
