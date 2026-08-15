// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ErrSetupTokenMismatch means a claim presented a token that is not the
// outstanding one. It is deliberately indistinguishable from ErrNoSetupToken at
// the HTTP edge: telling an unauthenticated caller which of the two happened
// tells them whether an installation is claimable and worth guessing at.
var ErrSetupTokenMismatch = errors.New("identity: setup token does not match")

// ErrSetupTokenExists means a token is already outstanding, so no new one was
// minted and the existing one is still the credential.
var ErrSetupTokenExists = errors.New("identity: a setup token is already outstanding")

// MintSetupToken issues the single-use credential that authorizes claiming an
// unprovisioned installation, returning the plaintext ONCE — only its hash is
// stored, so a database copy cannot be replayed into a claim.
//
// It refuses on an installation that already holds an organization. That is not
// belt-and-braces: SetupTokenOutstanding reports what this writes, so a token
// minted against a live installation would make it answer "claimable" to any
// stranger, and the SPA would render a claim screen for an installation that
// cannot be claimed.
//
// An outstanding token is kept, not replaced: a boot that silently minted a
// fresh one would invalidate the token an operator had already read out of the
// log and handed on. Under the installation advisory lock — the same one boot
// and claim take — so two api replicas starting together cannot both pass the
// EXISTS check and race each other into the unique index.
func (s *Service) MintSetupToken(ctx context.Context) (string, error) {
	return s.issueSetupToken(ctx, keepOutstanding)
}

// RotateSetupToken retires whatever is outstanding and issues a fresh
// credential, for the one case MintSetupToken cannot serve: a token lost before
// it was used. Without it the single-outstanding rule makes a lost token
// permanent — the installation stays unclaimable forever and only hand-written
// SQL against production gets it back.
//
// Deliberately NOT reachable over HTTP. It invalidates a live claim credential,
// which is exactly what an attacker wants when the operator holds one; ADR-0061
// §4 puts re-bootstrap on an operator-only CLI for the same reason, and this is
// that path.
//
// It refuses on a provisioned installation, where there is nothing to claim.
func (s *Service) RotateSetupToken(ctx context.Context) (string, error) {
	return s.issueSetupToken(ctx, replaceOutstanding)
}

// outstandingPolicy is what separates minting from rotating, and it is the only
// thing that does: whether an existing credential blocks the new one or is
// retired to make room for it.
type outstandingPolicy bool

const (
	// keepOutstanding — refuse rather than replace. A boot that silently minted
	// a fresh token would invalidate the one an operator had already read out
	// of the log and handed on.
	keepOutstanding outstandingPolicy = false
	// replaceOutstanding — retire first, so the old credential stops working
	// the moment this commits rather than both being live until one is spent.
	replaceOutstanding outstandingPolicy = true
)

// No audit_log or event_outbox row accompanies these writes, which is the one
// place in this package the standard write shape does not reach. It is a schema
// fact rather than a choice: audit_log.workspace_id, system_log.workspace_id and
// the outbox are all tenant-scoped and NOT NULL, and a setup token exists BEFORE
// the workspace it authorizes creating — there is no tenant to scope a record
// to. What the lifecycle does leave behind is the boot log line announcing the
// mint, and the system_log row the resulting claim writes inside the same
// transaction as the organization, naming the human who presented the token.
//
// issueSetupToken is the whole rule both public entry points apply: under the
// installation advisory lock, refuse a provisioned installation, settle what to
// do about an outstanding credential, and record only the hash of a new one.
//
// One body rather than two near-identical ones, because every line of it is
// security-bearing — the lock that stops two replicas racing, the provisioned
// refusal that stops /setup/status advertising a live installation as claimable,
// the hash-only write. A second copy is a second place for one of those to be
// dropped.
func (s *Service) issueSetupToken(ctx context.Context, policy outstandingPolicy) (string, error) {
	raw, hash, err := mintSessionToken()
	if err != nil {
		return "", fmt.Errorf("identity: minting the setup token: %w", err)
	}
	err = database.WithInfraTx(ctx, s.db.Pool(), func(tx pgx.Tx) error {
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
		if policy == replaceOutstanding {
			if err := retireSetupTokens(ctx, tx); err != nil {
				return err
			}
		} else {
			var outstanding bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM setup_token WHERE consumed_at IS NULL)`).Scan(&outstanding); err != nil {
				return fmt.Errorf("identity: checking for an outstanding setup token: %w", err)
			}
			if outstanding {
				return ErrSetupTokenExists
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO setup_token (token_hash) VALUES ($1)`, hash); err != nil {
			// The partial unique index is the real guarantee; the check above
			// only lets us say so in words. Report both the same way, so a boot
			// that loses a race it should not be in reports "already
			// outstanding" rather than dying on a raw constraint violation.
			if storekit.IsUniqueViolation(err) {
				return ErrSetupTokenExists
			}
			return fmt.Errorf("identity: recording the setup token: %w", err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

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

// retireSetupTokens marks every outstanding claim credential spent, in the
// caller's transaction. Idempotent and unconditional: an installation that
// holds an organization has nothing left to claim, so a token that survives it
// is a live credential with no legitimate use.
func retireSetupTokens(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx,
		`UPDATE setup_token SET consumed_at = now() WHERE consumed_at IS NULL`); err != nil {
		return fmt.Errorf("identity: retiring outstanding setup tokens: %w", err)
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
	// Refuse a provisioned installation WITHOUT taking the lock. This route is
	// unauthenticated and stays mounted for the life of the installation, so
	// the common case by far is a stranger reaching a live one; making that
	// path queue on the same advisory lock boot uses would let anyone stall
	// every other request behind a pool connection they hold for free. The
	// authoritative check still happens under the lock below — this one only
	// declines to pay for a question already answered.
	if cached := s.installation.Load(); cached != nil {
		return ids.WorkspaceID{}, ErrAlreadyProvisioned
	}
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
