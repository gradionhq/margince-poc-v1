// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The session token itself: how one is minted, how it is stored, and how a
// presented one is reduced to the value the row holds. Split out of service.go
// because it is one concept with one rule — the raw token exists only in the
// response, and only its hash is ever written.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func insertSession(ctx context.Context, tx pgx.Tx, userID ids.UserID, tokenHash string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO session (user_id, token_hash, idle_expires_at, expires_at)
		 VALUES ($1, $2, now() + $3::interval, now() + $4::interval)`,
		userID, tokenHash, idleTTL.String(), absoluteTTL.String())
	return err
}

// mintSessionToken returns the raw cookie value and the SHA-256 hex the
// database stores — the raw token never touches the DB (ADR-0043).
func mintSessionToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("crmauth: minting session token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
