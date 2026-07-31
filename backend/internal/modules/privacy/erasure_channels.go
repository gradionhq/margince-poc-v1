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

// personChannelIdentities reads every channel account bound to the subject,
// archived bindings included: an archived row still holds the provider account
// id and the handle, which identify the human exactly as a live row does, and
// archiving a Person archives their bindings (people/person.go), so a
// live-only read would erase nobody who had ever been archived.
//
// Called BEFORE anything downstream deletes person_channel_identity — both
// eraseChannelIdentities (which deletes the rows this returns) and
// purgeDerivedTraces (which needs the same identifiers to reach the subject's
// raw_capture rows) key off this one query, so there is exactly one spelling
// of "which accounts belong to this subject" in the erasure path.
func personChannelIdentities(ctx context.Context, tx pgx.Tx, personID ids.PersonID) ([]channelIdentity, error) {
	rows, err := tx.Query(ctx,
		`SELECT provider, channel_user_id FROM person_channel_identity WHERE person_id = $1`, personID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[channelIdentity])
}

// channelIdentityLockKeys renders the subject's accounts as the lock keys the
// ingest side takes on the very same accounts. It is a translation and not a
// second spelling of the identity: storekit owns the key, both callers hand it
// the same pair, so an erasure and a delivery cannot end up on different keys
// and silently stop excluding each other.
// channelActivityKeys renders the subject's accounts as the composite
// `provider:account` strings unlinkedSubjectChannel matches on. Composite
// rather than a bare account list on purpose: an account id is a numeric
// string, and matching one across every provider is the untyped over-deletion
// this module refuses elsewhere.
func channelActivityKeys(identities []channelIdentity) []string {
	keys := make([]string, 0, len(identities))
	for _, identity := range identities {
		keys = append(keys, identity.Provider+":"+identity.ChannelUserID)
	}
	return keys
}

func channelIdentityLockKeys(identities []channelIdentity) []storekit.ChannelIdentityKey {
	keys := make([]storekit.ChannelIdentityKey, 0, len(identities))
	for _, identity := range identities {
		keys = append(keys, storekit.ChannelIdentityKey{
			Provider: identity.Provider, ChannelUserID: identity.ChannelUserID,
		})
	}
	return keys
}

// eraseChannelIdentities removes the subject's channel identities and suppresses
// them, returning how many were suppressed for the erasure tombstone's counts.
// identities is the caller's OWN pre-erasure read (personChannelIdentities) —
// this function never re-queries, because the raw_capture purge in
// purgeDerivedTraces needs the identical rows read at the identical moment;
// two independent reads could observe different data under concurrent writes.
// Runs inside the caller's single erasure transaction: a delete that committed
// without its suppression row would leave the subject erasable-but-resurrectable,
// which is indistinguishable from a working erasure until the next message
// arrives.
//
// The delete resolves by ACCOUNT, not by person_id. uq_person_channel_identity
// is partial on archived_at IS NULL, so the same provider account can be bound
// by more than one Person row once an earlier binding is archived — and every
// one of those rows holds the erased human's account id and handle. Deleting
// only the subject's own rows would suppress and purge on an identifier that a
// sibling row goes on storing. refuseRivalIdentifierHolders (erasure_rivals.go)
// is what keeps this from reaching a LIVE Person's binding: this delete only
// ever also covers rows hanging off already-archived duplicates.
func eraseChannelIdentities(ctx context.Context, tx pgx.Tx, identities []channelIdentity) (int, error) {
	for _, identity := range identities {
		if _, err := tx.Exec(ctx,
			`DELETE FROM person_channel_identity WHERE provider = $1 AND channel_user_id = $2`,
			identity.Provider, identity.ChannelUserID); err != nil {
			return 0, err
		}
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

// purgeChannelRawCapture reaches raw_capture by channel identity, the
// counterpart to purgeDerivedTraces' email lane (erasure.go): a Telegram-only
// Person carries no email at all, so that lane never runs for them, and
// without this one their raw captures — the verbatim update JSON, including
// display name, username, numeric id and full message text — would survive
// Art. 17 erasure forever.
//
// The match is a typed JSONB path equality, never ILIKE. A Telegram sender id
// is a bare 9-10 digit integer; a substring search for it would also match
// against message ids, timestamps and other subjects' ids anywhere in ANY
// provider's payload, in the whole workspace's raw_capture table — an
// over-deleting erasure (another subject's evidence gone) is far worse than an
// under-deleting one, which is the entire reason the email lane beside this
// one accepts ILIKE at all: an email address is specific enough that the
// same risk does not apply to it.
//
// Both payload shapes below are matched because both land in raw_capture
// under the SAME source_system: capture/telegram's Normalize reads the sender
// from message.from.id for an ordinary message, and ParseMembership reads it
// from my_chat_member.chat.id for a block/unblock report — the webhook
// persists the raw update before anything classifies which kind it is
// (compose/telegramwebhook.go), so an unerased row of either shape would
// still resolve back onto this subject's account.
//
// The membership arm reads the CHAT and not new_chat_member.user, which is
// the bot: my_chat_member reports a change to the bot's own membership
// (capture/telegram/membership.go), so that user id belongs to no Person and
// an erasure keyed on it would purge nothing while appearing to cover the
// shape. A private chat's id IS the customer's own user id, which is exactly
// what person_channel_identity stores.
//
// ai_call_payload (erasure.go's purgeDerivedTraces) gets no equivalent lane:
// 0089's schema gives it no structural link to a subject at all, so the only
// possible match there would be a substring search for the same bare numeric
// id across every AI call in the workspace, not one provider's captures —
// strictly the over-deletion risk this function exists to avoid, only wider.
func purgeChannelRawCapture(ctx context.Context, tx pgx.Tx, identities []channelIdentity) (int64, error) {
	var purged int64
	for _, identity := range identities {
		tag, err := tx.Exec(ctx, `
			DELETE FROM raw_capture
			 WHERE source_system = $1
			   AND (payload->'message'->'from'->>'id' = $2
			        OR payload->'my_chat_member'->'chat'->>'id' = $2)`,
			identity.Provider, identity.ChannelUserID)
		if err != nil {
			return 0, err
		}
		purged += tag.RowsAffected()
	}
	return purged, nil
}
