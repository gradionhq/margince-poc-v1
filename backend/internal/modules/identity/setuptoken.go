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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
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
// fresh one would invalidate the token an operator had already taken from the
// token file and handed on. Under the installation advisory lock — the same one boot
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
	// a fresh token would invalidate the one an operator had already taken from
	// the token file and handed on.
	keepOutstanding outstandingPolicy = false
	// replaceOutstanding — retire first, so the old credential stops working
	// the moment this commits rather than both being live until one is spent.
	replaceOutstanding outstandingPolicy = true
)

// The lifecycle writes a system_log row for every act: minting a token,
// retiring an outstanding one, and — through the claim — spending it. It writes
// no audit_log or event_outbox row, and that half IS still a schema-shaped
// exemption: audit_log is the record-mutation spine (P12) and a setup token
// mutates no record, while an outbox envelope is a domain event nobody
// subscribes to before the installation exists.
//
// It used to write nothing at all, on the stated grounds that both ledgers
// carried a NOT NULL tenant column and a setup token exists BEFORE the
// organization it authorizes creating. ADR-0091 §8 phase D removed that column,
// so the impediment was gone and the gap was left. What actually stood in the
// way was the ACTOR: storekit.LogSystem refuses a caller with no principal
// bound, deliberately, because an unattributed ledger row is worse than none.
//
// So the lifecycle binds one. `system:setup-token` is the same shape a
// background pass uses when no human is present, and it is honest about what
// happened: nobody was authenticated, because nobody can be yet.
//
// What the row must NOT carry is the token, or its hash. The hash is the
// credential's stored form and a ledger is readable by every admin the
// installation will ever have.

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
	ctx = setupTokenActor(ctx)
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
		if _, err := storekit.LogSystem(ctx, tx, actionInstallationClaimOpened, map[string]any{
			"replaced_outstanding": policy == replaceOutstanding,
		}); err != nil {
			return fmt.Errorf("identity: recording that a setup token was issued: %w", err)
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
	tag, err := tx.Exec(ctx,
		`UPDATE setup_token SET consumed_at = now() WHERE consumed_at IS NULL`)
	if err != nil {
		return fmt.Errorf("identity: retiring outstanding setup tokens: %w", err)
	}
	// Only when one was actually retired. A rotation that found nothing
	// outstanding invalidated nobody's credential, and a row saying otherwise
	// would send an operator looking for a token that never existed.
	if tag.RowsAffected() == 0 {
		return nil
	}
	// The bootstrap paths reach here BEFORE they bind the actor that created
	// the organization, and that is the right order: the comment at the call
	// site says the ORGANIZATION retires the token, not whichever path made it.
	// So the retirement is a system act unless a caller has already said
	// otherwise, which the mint path has.
	ctx = ensureSetupTokenActor(ctx)
	if _, err := storekit.LogSystem(ctx, tx, actionInstallationClaimClosed, map[string]any{
		"retired": tag.RowsAffected(),
	}); err != nil {
		return fmt.Errorf("identity: recording that a setup token was retired: %w", err)
	}
	return nil
}

// The two actions the token's own lifecycle records. Named constants because
// each is also the string a reader greps the ledger for.
//
// They name the ACT — the installation became claimable, and stopped being —
// rather than the secret that carries it. Partly because that is the better
// vocabulary for an operator reading the ledger, and partly because gosec reads
// any identifier pairing `token` or `credential` with a string literal as a
// hardcoded secret. It is not wrong to look: a constant beside this code is
// exactly where a leaked one would sit.
const (
	actionInstallationClaimOpened = "installation_claim_opened"
	actionInstallationClaimClosed = "installation_claim_closed"
)

// setupTokenActor binds the principal the token's lifecycle acts under. Nobody
// is authenticated before an installation exists, so the ledger says so rather
// than borrowing an identity that was not there.
func setupTokenActor(ctx context.Context) context.Context {
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:setup-token",
	})
}

// ensureSetupTokenActor is setupTokenActor for a path that MAY already have a
// principal. It never overwrites one: a caller that knows who acted is always a
// better answer than "the system did it".
func ensureSetupTokenActor(ctx context.Context) context.Context {
	if _, err := storekit.Actor(ctx); err == nil {
		return ctx
	}
	return setupTokenActor(ctx)
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
func (s *Service) ClaimInstallation(ctx context.Context, token string, in InstallationBootstrap, seed func(ctx context.Context, tx pgx.Tx) error) (wsID ids.WorkspaceID, discarded []string, err error) {
	// Refuse a provisioned installation WITHOUT taking the lock. This route is
	// unauthenticated and stays mounted for the life of the installation, so
	// the common case by far is a stranger reaching a live one; making that
	// path queue on the same advisory lock boot uses would let anyone stall
	// every other request behind a pool connection they hold for free. The
	// authoritative check still happens under the lock below — this one only
	// declines to pay for a question already answered.
	if cached := s.installation.Load(); cached != nil {
		return ids.WorkspaceID{}, nil, ErrAlreadyProvisioned
	}
	ctx = setupTokenActor(ctx)
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
		if err := consumeSetupToken(ctx, tx, token); err != nil {
			return err
		}
		wsID, err = createInstallation(ctx, tx, in, originClaimed, seed, &discarded)
		return err
	})
	if err != nil {
		return ids.WorkspaceID{}, nil, err
	}
	s.installation.Store(&wsID)
	return wsID, discarded, nil
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
