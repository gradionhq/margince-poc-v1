// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The erasure-suppression probe (A13): erased subjects live on as
// hashes in erasure_suppression, and every ingest path that could
// resurrect one consults the SAME spelling — the eraser writes with
// SuppressionHash, capture reads with EmailSuppressed; a second
// hand-rolled hash would silently fork the list.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// SuppressionHash is the one identifier hashing rule: sha256 hex over
// the trimmed, lowercased value — writer and reader must normalize
// identically or a stray space resurrects an erased subject.
func SuppressionHash(value string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(digest[:])
}

// EscapeLike neutralizes LIKE/ILIKE wildcards in a value that is about
// to be embedded in a pattern (pair with ESCAPE '\'). An identifier
// containing % or _ must match itself, not everything — in an erasure
// purge an unescaped % would delete the whole evidence store.
func EscapeLike(value string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(value)
}

// EmailSuppressed reports whether an address belongs to an erased
// subject in the current workspace (RLS scopes the read).
func EmailSuppressed(ctx context.Context, tx pgx.Tx, email string) (bool, error) {
	var suppressed bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM erasure_suppression WHERE kind = 'email' AND value_hash = $1)`,
		SuppressionHash(email)).Scan(&suppressed)
	return suppressed, err
}

// ChannelIdentityHash is the suppression key for a messaging-channel
// identity: "provider:channel_user_id" under the hashing rule above,
// applied per FIELD before they are joined. The two sides read the value
// from different places — the eraser from the stored column, the ingest
// probe from a freshly parsed provider payload — so trimming only the
// joined string would let whitespace on one side alone fork the list.
//
// The bot (channel) id is deliberately absent. Telegram user ids are
// GLOBAL rather than bot-scoped, so keying on the bot would make an
// erasure stop holding the moment the workspace rotated its bot — the
// erased subject's next message would resurrect them, with nothing
// erroring and nothing logged. person_channel_identity's unique key omits
// the bot id for the same reason (0152).
func ChannelIdentityHash(provider, channelUserID string) string {
	return SuppressionHash(strings.TrimSpace(provider) + ":" + strings.TrimSpace(channelUserID))
}

// ChannelIdentitySuppressed reports whether a channel identity belongs to
// an erased subject in the current workspace (RLS scopes the read). It is
// the channel twin of EmailSuppressed: an ingest path that can create or
// re-bind a Person from an inbound message consults it first.
func ChannelIdentitySuppressed(ctx context.Context, tx pgx.Tx, provider, channelUserID string) (bool, error) {
	var suppressed bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM erasure_suppression WHERE kind = 'channel_identity' AND value_hash = $1)`,
		ChannelIdentityHash(provider, channelUserID)).Scan(&suppressed)
	return suppressed, err
}

// ChannelIdentityKey names one channel account for the lock below: the same
// (provider, channel_user_id) pair the hash and the probe above are built from.
type ChannelIdentityKey struct {
	Provider      string
	ChannelUserID string
}

// LockChannelIdentities blocks until the calling transaction exclusively owns
// every named account, and holds them until it ends. It is the mutex between
// an erasure and an ingest of the SAME human, and both sides must take it or
// neither is protected.
//
// The probe above cannot carry that on its own. Postgres runs these
// transactions at READ COMMITTED, so an ingest that probes, finds nothing, and
// then writes can have a whole erasure commit between its two statements: the
// row it goes on to write names a subject whose suppression is already armed,
// which guarantees person_channel_identity is never recreated — and every lane
// that could reach that row later (the erasure raw purge, the SAR raw section)
// drives off exactly those rows. Re-probing after the write narrows the window
// without closing it, because the erasure's own purge has already run by the
// time it commits. Serializing the two is the only answer that holds.
//
// The key is per workspace and per account, so two humans' deliveries never
// wait on each other and an erasure only ever stalls the subject it is erasing.
// hashtextextended is Postgres' own hash of the workspace-qualified identity
// hash, so the key is derived in ONE place for both callers rather than in Go
// on one side and SQL on the other.
//
// The accounts are locked in a FIXED order, deduplicated: two transactions
// taking the same pair in opposite orders would deadlock, and Postgres would
// resolve that by killing one of them — an erasure or a customer's message
// lost to an ordering nobody chose.
func LockChannelIdentities(ctx context.Context, tx pgx.Tx, keys []ChannelIdentityKey) error {
	hashes := make([]string, 0, len(keys))
	for _, key := range keys {
		hashes = append(hashes, ChannelIdentityHash(key.Provider, key.ChannelUserID))
	}
	slices.Sort(hashes)
	for _, hash := range slices.Compact(hashes) {
		if _, err := tx.Exec(ctx, `
			SELECT pg_advisory_xact_lock(hashtextextended(
				coalesce(current_setting('app.workspace_id', true), '') || ':' || $1, 0))`,
			hash); err != nil {
			return fmt.Errorf("storekit: locking a channel identity against a concurrent erasure: %w", err)
		}
	}
	return nil
}
