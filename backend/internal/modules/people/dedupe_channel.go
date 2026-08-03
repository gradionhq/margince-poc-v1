// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The two exact lanes a messaging channel needs, siblings of
// exactPersonByEmail (dedupe.go): a previously established channel binding,
// and an E.164 phone number. Both share the email lane's contract — live
// rows only, lowest person id on a tie, so the same candidate resolves the
// same way on every run.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// exactPersonByChannelIdentity is the ladder's first lane: a
// (provider, channel_user_id) pair already bound to a live person.
//
// blocked_at is deliberately not read. Blocking is reachability — the fact
// "telegram user 123 is person A" stays true while the user has the bot
// blocked, and a lane that missed on it would create a SECOND person the
// moment they unblocked and wrote again, which the partial unique index
// (0146) happily admits.
func exactPersonByChannelIdentity(ctx context.Context, tx pgx.Tx, identities []connector.ChannelIdentity) (ids.PersonID, bool, error) {
	if len(identities) == 0 {
		return ids.PersonID{}, false, nil
	}
	providers := make([]string, 0, len(identities))
	channelUserIDs := make([]string, 0, len(identities))
	for _, ci := range identities {
		if ci.Provider == "" || ci.ChannelUserID == "" {
			// Half a key is no key: it can only match by accident, and the
			// unique index never admitted such a row in the first place.
			continue
		}
		providers = append(providers, ci.Provider)
		channelUserIDs = append(channelUserIDs, ci.ChannelUserID)
	}
	if len(providers) == 0 {
		return ids.PersonID{}, false, nil
	}
	var id ids.PersonID
	err := tx.QueryRow(ctx, `
		SELECT pci.person_id
		  FROM person_channel_identity pci
		  JOIN unnest($1::text[], $2::text[]) AS k(provider, channel_user_id)
		    ON k.provider = pci.provider AND k.channel_user_id = pci.channel_user_id
		 WHERE pci.archived_at IS NULL
		 ORDER BY pci.person_id
		 LIMIT 1`, providers, channelUserIDs).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.PersonID{}, false, nil
	}
	if err != nil {
		return ids.PersonID{}, false, fmt.Errorf("dedupe person channel-identity tier: %w", err)
	}
	return id, true, nil
}

// exactPersonByPhone matches on the E.164 form person_phone stores. The
// candidate side is normalized here so the comparison is like for like;
// a number that cannot be normalized is dropped rather than compared,
// because it can never equal a stored E.164 value and refusing the whole
// resolution over one malformed provider field would drop the message.
func exactPersonByPhone(ctx context.Context, tx pgx.Tx, phones []string) (ids.PersonID, bool, error) {
	if len(phones) == 0 {
		return ids.PersonID{}, false, nil
	}
	normalized := make([]string, 0, len(phones))
	for _, raw := range phones {
		parsed, err := values.ParsePhone(raw)
		if err != nil {
			continue
		}
		normalized = append(normalized, parsed.String())
	}
	if len(normalized) == 0 {
		return ids.PersonID{}, false, nil
	}
	var id ids.PersonID
	err := tx.QueryRow(ctx, `
		SELECT person_id FROM person_phone
		WHERE phone = ANY($1) AND archived_at IS NULL
		ORDER BY person_id
		LIMIT 1`, normalized).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.PersonID{}, false, nil
	}
	if err != nil {
		return ids.PersonID{}, false, fmt.Errorf("dedupe person phone tier: %w", err)
	}
	return id, true, nil
}
