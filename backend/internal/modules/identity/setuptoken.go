// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ErrNoSetupToken means no unconsumed setup token exists: either this
// installation was never minted one (it is provisioned, or configured with a
// bootstrap_admin) or the token has already been spent.
var ErrNoSetupToken = errors.New("identity: no outstanding setup token")

// ErrSetupTokenMismatch means a claim presented a token that is not the
// outstanding one. It is deliberately indistinguishable from ErrNoSetupToken at
// the HTTP edge: telling an unauthenticated caller which of the two happened
// tells them whether an installation is claimable and worth guessing at.
var ErrSetupTokenMismatch = errors.New("identity: setup token does not match")

// MintSetupToken issues the single-use credential that authorizes claiming an
// unprovisioned installation, returning the plaintext ONCE — only its hash is
// stored, so a database copy cannot be replayed into a claim.
//
// Minting is idempotent by omission rather than by upsert: when a token is
// already outstanding it returns ErrSetupTokenExists and keeps the existing
// one, because a boot that silently replaced it would invalidate the token an
// operator had already read out of the log and handed on.
func (s *Service) MintSetupToken(ctx context.Context) (raw string, err error) {
	raw, hash, err := mintSessionToken()
	if err != nil {
		return "", err
	}
	err = database.WithInfraTx(ctx, s.db.Pool(), func(tx pgx.Tx) error {
		var outstanding bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM setup_token WHERE consumed_at IS NULL)`).Scan(&outstanding); err != nil {
			return fmt.Errorf("identity: checking for an outstanding setup token: %w", err)
		}
		if outstanding {
			return ErrSetupTokenExists
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO setup_token (token_hash) VALUES ($1)`, hash); err != nil {
			return fmt.Errorf("identity: recording the setup token: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

// ErrSetupTokenExists means a token is already outstanding, so no new one was
// minted and the existing one is still the credential.
var ErrSetupTokenExists = errors.New("identity: a setup token is already outstanding")

// consumeSetupToken spends the outstanding token, refusing anything that is not
// it. It runs INSIDE the caller's transaction: consuming the token and creating
// the organization must commit together, or a failed claim would burn the
// credential and leave the installation unclaimable.
//
// The UPDATE carries the match in its WHERE clause rather than reading the row
// first and comparing in Go: two concurrent claims presenting the same valid
// token both pass a read-then-compare, and only the row lock decides. Here the
// second one updates nothing and is refused.
func consumeSetupToken(ctx context.Context, tx pgx.Tx, presented string) error {
	tag, err := tx.Exec(ctx,
		`UPDATE setup_token SET consumed_at = now()
		 WHERE consumed_at IS NULL AND token_hash = $1`, hashToken(presented))
	if err != nil {
		return fmt.Errorf("identity: consuming the setup token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSetupTokenMismatch
	}
	return nil
}

// ErrAlreadyProvisioned means a claim arrived at an installation that already
// holds an organization. It is reported as itself rather than as a token
// failure: a caller holding a valid token deserves the true reason, and the
// fact that an installation is provisioned is not a secret — every request to
// it already reveals that.
var ErrAlreadyProvisioned = errors.New("identity: installation is already provisioned")

// ClaimInstallation creates the organization and its first admin from a claim
// authorized by the setup token, in ONE transaction under the same advisory
// lock boot takes — so two concurrent claims cannot both succeed, and a claim
// racing a configured boot cannot produce a second organization.
//
// Consuming the token and creating the organization commit together. Spending
// it first and creating after would leave an installation unclaimable whenever
// creation failed — a mistyped currency would burn the only credential that
// could fix it.
//
// The provisioned check runs BEFORE the token is consumed, so a claim aimed at
// a live installation is refused without spending anything.
func (s *Service) ClaimInstallation(ctx context.Context, token string, in InstallationBootstrap, seed func(ctx context.Context, tx pgx.Tx) error) (ids.WorkspaceID, error) {
	var wsID ids.WorkspaceID
	err := database.WithInfraTx(ctx, s.db.Pool(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, installationLockKey); err != nil {
			return fmt.Errorf("identity: taking the bootstrap advisory lock: %w", err)
		}
		existing, err := activeWorkspaces(ctx, tx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return ErrAlreadyProvisioned
		}
		if err := consumeSetupToken(ctx, tx, token); err != nil {
			return err
		}
		wsID, err = createInstallation(ctx, tx, in, originClaimed, seed)
		return err
	})
	if err != nil {
		return ids.WorkspaceID{}, err
	}
	s.installation.Store(&wsID)
	return wsID, nil
}

// SetupTokenOutstanding reports whether this installation is waiting to be
// claimed. It answers a question an unauthenticated caller may ask — the SPA
// needs it to decide whether to offer the claim screen at all — so it discloses
// only that a token exists, never the token.
func (s *Service) SetupTokenOutstanding(ctx context.Context) (bool, error) {
	var outstanding bool
	err := database.WithInfraTx(ctx, s.db.Pool(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM setup_token WHERE consumed_at IS NULL)`).Scan(&outstanding)
	})
	if err != nil {
		return false, fmt.Errorf("identity: probing for an outstanding setup token: %w", err)
	}
	return outstanding, nil
}
