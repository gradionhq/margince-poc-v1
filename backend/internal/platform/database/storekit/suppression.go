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
