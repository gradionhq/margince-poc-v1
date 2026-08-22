// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

// How long an invitation credential stands before it lapses. A week is the same
// window a member invitation gets: long enough to survive a holiday, short
// enough that a link found in an old mailbox has usually stopped working.
const invitationTTL = 7 * 24 * time.Hour

// The prefix every Deal Room credential carries, so a leaked string is
// recognizable for what it is — in a log, a support ticket, or a secret scanner
// — rather than looking like anonymous base64.
const credentialPrefix = "mdr_"

// mintCredential returns the raw credential to mail and the digest to store.
//
// Only the digest reaches deal_room_invitation, so a dump of that table
// re-admits nobody. That is a claim about this table and not about the whole
// database, deliberately: the invite operation takes no Idempotency-Key
// precisely because the replay cache would hold the response — credential and
// all — in plaintext for a day, and a comment here saying "never reaches the
// database" would have been false the moment it did.
//
// The digest covers the PREFIXED string, which is what the wire carries, so a
// credential has one spelling and cannot be presented in one form and checked
// against another.
//
// This copies the shape identity uses for its session and reset tokens rather
// than importing it: a module may not import a sibling, and the credential
// vocabulary is small enough that the copy is the sanctioned form. What must
// NOT drift is the strength — 256 bits from crypto/rand — and the rule that
// only the digest is persisted.
func mintCredential() (raw string, digest []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("dealrooms: minting an invitation credential: %w", err)
	}
	raw = credentialPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, digestOfCredential(raw), nil
}

// digestOfCredential is the stored form of a credential.
//
// It returns the raw 32-byte digest rather than hex, because the columns are
// bytea. The comparison always happens in SQL — the digest goes in the WHERE
// clause, never read back and compared in Go — so there is no timing question
// to answer here and no constant-time compare to get wrong.
func digestOfCredential(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
