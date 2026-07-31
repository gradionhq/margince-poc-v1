// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Federated human sign-in (A107/ADR-0061 §6, §11) — the service half: the
// single-use login state, and the resolution of a validated provider
// identity onto a local human.
//
// The rule the whole file exists to keep: a federated login NEVER creates
// an account. It resolves `(issuer, subject)` to an existing binding, or —
// exactly once, for a human who has no binding for this issuer yet — it
// writes one after the provider's VERIFIED email matches a local user. From
// then on the subject is the identity; the email is only evidence of how
// the binding came to be, because a provider can change an address and the
// subject is designed not to change.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/oidc"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// oidcStateTTL bounds one authorization round-trip. Long enough for a human
// to pick an account and pass an MFA prompt at the provider; short because
// the row holds a live PKCE verifier.
const oidcStateTTL = 10 * time.Minute

// The two ways a validated provider identity can still fail to become a
// session. Both are ONE neutral answer on the login screen (`not_linked`) —
// separating them there would confirm which addresses exist — and two
// distinct system_log entries for the operator.
var (
	// errNoLinkableUser: no active or invited local human holds the verified
	// address.
	errNoLinkableUser = errors.New("identity: no local user matches the verified provider email")
	// errUserBoundElsewhere: the address maps to a human who is already
	// bound to a DIFFERENT account at this issuer. An email match must never
	// silently relink an already-bound identity (§11).
	errUserBoundElsewhere = errors.New("identity: the matching user is already bound to another account at this issuer")
)

// StartOIDCLogin persists one outbound authorization attempt and returns
// nothing but the error: the caller already holds the secrets it minted.
// Only the state's HASH is stored — the raw value lives in the browser
// cookie and the provider round-trip, the same no-raw-credential-at-rest
// rule the session and reset tokens follow.
func (s *Service) StartOIDCLogin(ctx context.Context, providerKey string, req oidc.AuthRequest) error {
	wsID, ok := workspaceFrom(ctx)
	if !ok {
		return apperrors.ErrNotFound
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Sweep this installation's dead attempts on the way past — consumed or
		// not, because a spent attempt still holds its PKCE verifier and there
		// is nothing in it worth keeping. Doing it here means the flow needs no
		// background job to stay clean.
		if _, err := tx.Exec(ctx,
			`DELETE FROM oidc_login_state WHERE expires_at < now()`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO oidc_login_state (workspace_id, provider, state_hash, nonce, code_verifier, expires_at)
			 VALUES ($1, $2, $3, $4, $5, now() + $6::interval)`,
			wsID, providerKey, hashToken(req.State), req.Nonce, req.Verifier, oidcStateTTL.String())
		return err
	})
}

// oidcAttempt is a claimed login state: the two secrets the callback needs
// to finish, which exist nowhere the browser could have supplied them.
type oidcAttempt struct {
	Nonce    string
	Verifier string
}

// ClaimOIDCLoginState consumes the state single-use and returns the
// attempt. Unknown, already-consumed, expired, and wrong-provider states
// are one apperrors.ErrNotFound: the callback answers them identically, so
// a state cannot be probed for which failure it is.
func (s *Service) ClaimOIDCLoginState(ctx context.Context, providerKey, rawState string) (oidcAttempt, error) {
	if _, ok := workspaceFrom(ctx); !ok {
		return oidcAttempt{}, apperrors.ErrNotFound
	}
	var attempt oidcAttempt
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// The claim IS the update: consuming and reading in one statement
		// leaves no window in which two concurrent callbacks both see an
		// unconsumed row.
		err := tx.QueryRow(ctx,
			`UPDATE oidc_login_state SET consumed_at = now()
			 WHERE state_hash = $1 AND provider = $2 AND consumed_at IS NULL AND now() < expires_at
			 RETURNING nonce, code_verifier`,
			hashToken(rawState), providerKey).Scan(&attempt.Nonce, &attempt.Verifier)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		return err
	})
	if err != nil {
		return oidcAttempt{}, err
	}
	return attempt, nil
}

// CompleteOIDCLogin turns a validated provider identity into a session and
// returns the raw session token. It resolves the human by
// `(issuer, subject)`, writing the binding on the first verified-email match,
// and mints the same opaque session password login does — a federated human
// is not a second class of principal.
//
// It returns the token and nothing else, unlike Login: the callback answers a
// 302 with no body, so the client reads its identity from /me afterwards, and
// resolving roles and teams here would be work for a response that does not
// exist.
func (s *Service) CompleteOIDCLogin(ctx context.Context, external oidc.Identity) (string, error) {
	wsID, ok := workspaceFrom(ctx)
	if !ok {
		// Pre-bootstrap there is no human to sign in; nothing about the
		// installation's state is disclosed beyond "not linked".
		return "", errNoLinkableUser
	}
	token, tokenHash, err := mintSessionToken()
	if err != nil {
		return "", err
	}

	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		userID, linked, err := resolveExternalIdentity(ctx, tx, wsID, external)
		if err != nil {
			return err
		}
		if err := requireSignInReady(ctx, tx, userID); err != nil {
			return err
		}
		if err := insertSession(ctx, tx, wsID, userID, tokenHash); err != nil {
			return err
		}
		detail := "federated sign-in (" + external.Issuer + ")"
		if linked {
			detail = "federated sign-in (" + external.Issuer + "); provider identity linked on first verified-email match"
		}
		return auditLogin(ctx, tx, wsID, userID, detail)
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// resolveExternalIdentity maps the provider identity onto a local human,
// reporting whether the binding was written by this call. An existing
// binding always wins: an email match may never move a subject that is
// already bound.
func resolveExternalIdentity(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, external oidc.Identity) (ids.UserID, bool, error) {
	var userID ids.UserID
	err := tx.QueryRow(ctx,
		`UPDATE external_identity SET last_authenticated_at = now()
		 WHERE issuer = $1 AND subject = $2
		 RETURNING user_id`,
		external.Issuer, external.Subject).Scan(&userID)
	if err == nil {
		return userID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ids.UserID{}, false, err
	}
	return linkExternalIdentity(ctx, tx, wsID, external)
}

// linkExternalIdentity writes the first — and permanent — binding for a
// human who has none at this issuer. The verified email is the one-time
// allowlist here and nowhere else afterwards.
//
// An `invited` human is activated by the link: a member provisioned by an
// admin (A97), and the pending administrator a §11 bootstrap creates, both
// carry no password, and completing the provider flow is exactly the proof
// of address control their activation waits for.
func linkExternalIdentity(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, external oidc.Identity) (ids.UserID, bool, error) {
	var userID ids.UserID
	var boundAlready bool
	err := tx.QueryRow(ctx,
		`SELECT u.id, EXISTS (SELECT 1 FROM external_identity e WHERE e.user_id = u.id AND e.issuer = $2)
		 FROM app_user u
		 WHERE u.email = lower($1) AND u.archived_at IS NULL AND u.status IN ('active', 'invited')
		 FOR UPDATE`,
		external.Email, external.Issuer).Scan(&userID, &boundAlready)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.UserID{}, false, errNoLinkableUser
	}
	if err != nil {
		return ids.UserID{}, false, err
	}
	if boundAlready {
		return ids.UserID{}, false, errUserBoundElsewhere
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO external_identity (workspace_id, user_id, issuer, subject, email_at_link_time, last_authenticated_at)
		 VALUES ($1, $2, $3, $4, lower($5), now())`,
		wsID, userID, external.Issuer, external.Subject, external.Email); err != nil {
		// The unique indexes are the race arbiter, and the loser is refused as
		// a refusal rather than as a fault: two simultaneous first logins reach
		// the INSERT together (the row lock above serializes them, but each
		// evaluated its "already bound?" test against its own snapshot), and
		// the same constraint answers a subject already bound in another
		// workspace, which this workspace-scoped read cannot see. Both are
		// "this identity is not yours to claim" — the callback renders
		// not_linked, never a 500 mid-navigation.
		if storekit.IsUniqueViolation(err) {
			return ids.UserID{}, false, errUserBoundElsewhere
		}
		return ids.UserID{}, false, fmt.Errorf("identity: linking provider identity: %w", err)
	}
	// Activation is part of the same transaction as the binding: a human can
	// never end up active with no way to sign in, or bound but still pending.
	if _, err := tx.Exec(ctx,
		`UPDATE app_user SET status = 'active' WHERE id = $1 AND status = 'invited'`, userID); err != nil {
		return ids.UserID{}, false, err
	}
	if err := logAuthEvent(ctx, tx, wsID, userID, "oidc_identity_linked",
		"provider identity bound to "+external.Issuer+" on first verified-email match"); err != nil {
		return ids.UserID{}, false, err
	}
	return userID, true, nil
}

// requireSignInReady re-reads the human AFTER the binding and refuses anyone
// who may not hold a session right now. It is the ONE place both federated
// paths — the existing binding and the just-written one — pass through, so
// the state rules cannot be honored on one and missed on the other.
//
// Two rules, both shared with the password path: the account must be active
// and unarchived (so an account deactivated between the provider round-trip
// and this transaction cannot still be let in), and a §27 lockout refuses the
// sign-in. The lock is deliberately NOT password-specific — an admin locking
// an account expects it locked, and a second door that ignores it would make
// the first one decorative. Its refusal is `errNoLinkableUser`, which the
// callback renders as the same neutral `not_linked` as every other miss: the
// login screen must not become an oracle for account state.
func requireSignInReady(ctx context.Context, tx pgx.Tx, userID ids.UserID) error {
	// The lock is judged in SQL, against the database's clock — the same place
	// and the same way the session and passport expiries are.
	var locked bool
	err := tx.QueryRow(ctx,
		`SELECT locked_until IS NOT NULL AND now() < locked_until FROM app_user
		 WHERE id = $1 AND status = 'active' AND archived_at IS NULL`,
		userID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNoLinkableUser
	}
	if err != nil {
		return err
	}
	if locked {
		return errNoLinkableUser
	}
	return nil
}
