// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// doiTokenTTL bounds the confirmation window: an unclicked confirmation
// mail is a refusal, not a standing credential.
const doiTokenTTL = 72 * time.Hour

// IssueDoubleOptIn mints the single-use confirmation token a DOI grant
// must later present. Only the sha256 lands in the database — the
// session/passport secret discipline — so a stolen table cannot confirm
// anything. Delivering the plaintext to the data subject is the
// caller's concern: the contract defines no mint/delivery endpoint yet
// (filed as feedback), so this store seam is the one issuance path.
func (s *Store) IssueDoubleOptIn(ctx context.Context, personID, purposeID ids.UUID) (string, error) {
	if err := auth.Require(ctx, "person", principal.ActionUpdate); err != nil {
		return "", err
	}
	token, err := newDOIToken()
	if err != nil {
		return "", err
	}
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "person", personID); err != nil {
			return err
		}
		var requiresDOI bool
		err := tx.QueryRow(ctx,
			`SELECT requires_double_opt_in FROM consent_purpose WHERE id = $1 AND archived_at IS NULL`,
			purposeID).Scan(&requiresDOI)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("purpose %s: %w", purposeID, apperrors.ErrNotFound)
		}
		if err != nil {
			return err
		}
		if !requiresDOI {
			return &ValidationError{Field: "purpose_id", Reason: "purpose does not require a double opt-in"}
		}
		issued := s.now().UTC()
		_, err = tx.Exec(ctx, `
			INSERT INTO consent_doi_token (workspace_id, person_id, purpose_id, token_hash, issued_at, expires_at)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, $4, $5)`,
			personID, purposeID, hashDOIToken(token), issued, issued.Add(doiTokenTTL))
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// consumeDOIToken redeems the round-trip proof exactly once. The grant
// is only as strong as the token the confirmation mail carried, so a
// value that was never issued, was already used, belongs to another
// person×purpose, or has expired refuses the grant instead of recording
// a half-true confirmation.
func (s *Store) consumeDOIToken(ctx context.Context, tx pgx.Tx, personID, purposeID ids.UUID, token string) (time.Time, error) {
	confirmed := s.now().UTC()
	var id ids.UUID
	err := tx.QueryRow(ctx, `
		UPDATE consent_doi_token SET consumed_at = $1
		WHERE person_id = $2 AND purpose_id = $3 AND token_hash = $4
		  AND consumed_at IS NULL AND expires_at > $1
		RETURNING id`,
		confirmed, personID, purposeID, hashDOIToken(token)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, &ValidationError{Field: "double_opt_in_token", Reason: "not a currently issued double opt-in token"}
	}
	if err != nil {
		return time.Time{}, err
	}
	return confirmed, nil
}

func newDOIToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("consent: doi token entropy: %w", err)
	}
	return "doi_" + base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func hashDOIToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
