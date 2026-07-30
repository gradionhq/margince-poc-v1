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
