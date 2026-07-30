// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Refresh-token rotation: spending the token a connector presents and issuing
// the successor pair in its place. The grant record itself, and the cascade
// that ends a connection, live in oauth_grant.go — including the lock order
// this file obeys.

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Refresh tokens are stored HASHED, so a lost response can never be replayed —
// the plaintext successor is gone. Revoking the chain on every replay would
// therefore destroy a healthy connector whenever a response was lost in
// transit, which is indistinguishable from theft at the wire. So: a consumed
// token presented within the grace window whose successor is itself unconsumed
// is a lost-response retry — refuse it WITHOUT revoking, leaving the live
// access token working until its own expiry. Anything else (outside the window,
// or a successor already in use) is genuine reuse: revoke the grant, the whole
// chain, and every passport under it (RFC 9700).
const refreshReplayGrace = 30 * time.Second

var (
	// errRefreshRejected is every refusal that leaves the store untouched:
	// an unknown, expired or foreign token, a dead grant or client, a grant
	// that never allowed refresh, and the lost-response retry. One sentinel
	// because the answer is one answer — the endpoint must not turn the
	// difference into an oracle for whoever is presenting the token.
	errRefreshRejected = errors.New("oauth: refresh token rejected")
	// errRefreshReuse is a consumed token presented outside the grace window
	// or against a successor already spent: theft, so the connection dies. It
	// never reaches the transport — the cascade commits and the caller answers
	// errRefreshRejected, since victim and thief get the same answer.
	errRefreshReuse = errors.New("oauth: refresh token reused")
	// errRefreshScope is a renewal asking for authority the human never
	// approved.
	errRefreshScope = errors.New("oauth: requested scope exceeds the grant")
)

// refreshRequest is a presented refresh_token grant as it arrived on the
// wire. CanonicalResource is this installation's own MCP endpoint, injected
// from configuration, so the RFC 8707 audience decision never depends on a
// header the caller controls.
type refreshRequest struct {
	Token             string
	ClientID          string
	Scopes            []string
	Resource          string
	CanonicalResource string
}

// lockedGrant is the presented refresh row together with the consent above
// it and the client it was approved for, read under the rotation lock —
// everything the decision needs, so no follow-up query can observe a state
// the lock exists to freeze.
type lockedGrant struct {
	tokenID     ids.UUID
	consumedAt  *time.Time
	expiresAt   time.Time
	replacedBy  *ids.UUID
	workspaceID ids.WorkspaceID

	grantID        ids.UUID
	userID         ids.UserID
	clientID       string
	scopes         []string
	resource       *string
	grantRevokedAt *time.Time
	refreshAllowed bool

	clientDisabledAt *time.Time
	clientDeletedAt  *time.Time
}

// identity is the human whose authority the renewed passport borrows — the
// one who consented, not whoever presented the token.
func (l lockedGrant) identity() Identity {
	return Identity{UserID: l.userID, WorkspaceID: l.workspaceID}
}

// rotateRefreshToken spends a refresh token and issues its successor in ONE
// transaction that opens by locking the grant and THEN the token — the lock
// order stated in oauth_grant.go, which revokeGrantTx takes too. Every
// concurrent presentation of the same token queues behind the winner and sees
// it already consumed, and a racing revoke serializes against it rather than
// interleaving with the reissue or deadlocking with it. A read-then-write here
// would mint a successor per presentation, leaving a connector holding
// divergent chains.
func (s *Service) rotateRefreshToken(ctx context.Context, in refreshRequest) (IssuedPassport, string, error) {
	var (
		issued  IssuedPassport
		refresh string
		reused  bool
	)
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		tokenHash := hashToken(in.Token)
		grantID, err := grantOfPresentedToken(ctx, tx, tokenHash)
		if err != nil {
			return err
		}
		if err := lockGrant(ctx, tx, grantID); err != nil {
			return err
		}
		locked, err := lockPresentedRefreshToken(ctx, tx, tokenHash)
		if err != nil {
			return err
		}
		// Every write below is attributed to the human who consented: a
		// renewal has no session, and an unattributed audit row would hide
		// whose connection changed.
		writeCtx := actorCtx(ctx, locked.identity())
		switch err := presentationVerdict(ctx, tx, locked, in, s.now()); {
		case errors.Is(err, errRefreshReuse):
			if err := s.revokeGrantTx(writeCtx, tx, locked.grantID, reuseRevokeReason); err != nil {
				return err
			}
			// The cascade MUST commit: returning the refusal from here would
			// roll it back and leave the stolen chain alive. So the
			// transaction succeeds and the refusal is answered after it.
			reused = true
			return nil
		case err != nil:
			return err
		}
		scopes, err := narrowedScopes(in.Scopes, locked.scopes)
		if err != nil {
			return err
		}
		issued, refresh, err = spendAndReissue(writeCtx, tx, locked, scopes)
		return err
	})
	switch {
	case err != nil:
		return IssuedPassport{}, "", err
	case reused:
		return IssuedPassport{}, "", errRefreshRejected
	}
	return issued, refresh, nil
}

// grantOfPresentedToken names WHICH connection a presented token belongs to,
// deliberately without a lock: grant_id on a refresh row is immutable, so this
// read cannot go stale in any way that matters, and its only job is to name
// the row that must be locked FIRST.
//
// Do not fold it back into the joined SELECT below. One joined
// `SELECT … FOR UPDATE` locks the refresh row before the grant — the planner
// drives the join from the token_hash index — which is the opposite of the
// lock order in oauth_grant.go and deadlocks against revokeGrantTx (proven:
// oauth_lockorder_integration_test.go reproduces the deadlock when this step
// is removed). The unlocked read is
// safe precisely because it decides nothing: every decision is made from the
// authoritative read taken afterwards, under the grant lock.
func grantOfPresentedToken(ctx context.Context, tx pgx.Tx, tokenHash string) (ids.UUID, error) {
	var grantID ids.UUID
	err := tx.QueryRow(ctx,
		`SELECT grant_id FROM oauth_refresh_token WHERE token_hash = $1`, tokenHash).Scan(&grantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, errRefreshRejected
	}
	if err != nil {
		return ids.Nil, err
	}
	return grantID, nil
}

// lockGrant takes the connection-level lock, always before any refresh row.
// It reads no columns on purpose: the authoritative read of the grant's state
// is the joined SELECT that follows, under this lock. A grant that vanished
// between the two reads leaves nothing to renew.
func lockGrant(ctx context.Context, tx pgx.Tx, grantID ids.UUID) error {
	var locked ids.UUID
	err := tx.QueryRow(ctx,
		`SELECT id FROM oauth_grant WHERE id = $1 FOR UPDATE`, grantID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return errRefreshRejected
	}
	return err
}

// lockPresentedRefreshToken is the authoritative read: the presented token,
// the consent above it and the client it was approved for, all under lock. The
// grant's lock is already held (lockGrant), so the only lock this statement
// adds is the refresh row's — which is what keeps the order above intact.
func lockPresentedRefreshToken(ctx context.Context, tx pgx.Tx, tokenHash string) (lockedGrant, error) {
	var l lockedGrant
	err := tx.QueryRow(ctx, `
		SELECT r.id, r.consumed_at, r.expires_at, r.replaced_by, r.workspace_id,
		       g.id, g.user_id, g.client_id, g.scopes, g.resource, g.revoked_at, g.refresh_allowed,
		       c.disabled_at, c.deleted_at
		  FROM oauth_refresh_token r
		  JOIN oauth_grant  g ON (g.workspace_id, g.id)        = (r.workspace_id, r.grant_id)
		  JOIN oauth_client c ON (c.workspace_id, c.client_id) = (g.workspace_id, g.client_id)
		 WHERE r.workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		   AND r.token_hash = $1
		   FOR UPDATE OF r, g`,
		tokenHash).Scan(&l.tokenID, &l.consumedAt, &l.expiresAt, &l.replacedBy, &l.workspaceID,
		&l.grantID, &l.userID, &l.clientID, &l.scopes, &l.resource, &l.grantRevokedAt, &l.refreshAllowed,
		&l.clientDisabledAt, &l.clientDeletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedGrant{}, errRefreshRejected
	}
	if err != nil {
		return lockedGrant{}, err
	}
	return l, nil
}

// presentationVerdict decides, inside the lock, what this presentation is:
// nil to rotate, errRefreshReuse for the cascade, any other error to refuse
// and touch nothing.
//
// Liveness comes first, so a connection that is already dead answers the same
// refusal for every token under it and is never re-read as theft. now is the
// service clock, which is why the grace-window transition is provable without
// waiting for it.
func presentationVerdict(ctx context.Context, tx pgx.Tx, l lockedGrant, in refreshRequest, now time.Time) error {
	switch {
	case l.grantRevokedAt != nil, l.clientDisabledAt != nil, l.clientDeletedAt != nil,
		!l.refreshAllowed, l.clientID != in.ClientID, !now.Before(l.expiresAt):
		return errRefreshRejected
	}
	if !refreshAudienceMatches(in.Resource, in.CanonicalResource, l.resource) {
		return errRefreshRejected
	}
	if l.consumedAt == nil {
		return nil
	}
	// A consumed token with no forward link succeeded nothing, so there is no
	// lost response it could be a retry of (the chain was closed by a
	// revoke): reuse.
	if l.replacedBy == nil {
		return errRefreshReuse
	}
	var successorConsumed *time.Time
	if err := tx.QueryRow(ctx,
		`SELECT consumed_at FROM oauth_refresh_token WHERE id = $1`, *l.replacedBy).
		Scan(&successorConsumed); err != nil {
		return err
	}
	if successorConsumed == nil && now.Sub(*l.consumedAt) < refreshReplayGrace {
		return errRefreshRejected // the lost-response retry: refuse, revoke nothing
	}
	return errRefreshReuse
}

// narrowedScopes resolves what the successor passport carries: a renewal may
// ask for less than the human approved and never for more (RFC 6749 §6 — the
// grant is the ceiling, and narrowing once is not a ratchet), and asking for
// nothing carries the grant's scopes forward. offline_access is dropped
// rather than refused because clients echo the scope string they authorized
// with, and the marker's home is the grant's refresh_allowed — it is never a
// passport scope.
func narrowedScopes(requested, granted []string) ([]string, error) {
	narrowed := make([]string, 0, len(requested))
	for _, sc := range requested {
		if sc == scopeOfflineAccess {
			continue
		}
		if !slices.Contains(granted, sc) {
			return nil, errRefreshScope
		}
		narrowed = append(narrowed, sc)
	}
	if len(narrowed) == 0 {
		return granted, nil
	}
	return narrowed, nil
}

// spendAndReissue writes everything that replaces the presented token: the
// row is consumed, the successor is inserted and linked back from it, the
// passports the token minted are retired and one fresh passport takes their
// place. All of it in the caller's transaction, so no commit can leave a
// connector holding two live passports, or a successor whose predecessor is
// still spendable.
func spendAndReissue(ctx context.Context, tx pgx.Tx, l lockedGrant, scopes []string) (IssuedPassport, string, error) {
	// Conditional UPDATE with the row count asserted: belt-and-braces BEHIND
	// the lock, not instead of it — the same shape consumeAuthCode uses to
	// keep a single-use credential single-use.
	tag, err := tx.Exec(ctx,
		`UPDATE oauth_refresh_token SET consumed_at = now() WHERE id = $1 AND consumed_at IS NULL`,
		l.tokenID)
	if err != nil {
		return IssuedPassport{}, "", err
	}
	if tag.RowsAffected() != 1 {
		return IssuedPassport{}, "", errRefreshRejected
	}

	raw, err := randomToken()
	if err != nil {
		return IssuedPassport{}, "", err
	}
	// The hash covers the PREFIXED token, exactly as issueGrant stores the
	// first one in the chain.
	refresh := refreshTokenPrefix + raw
	var successorID ids.UUID
	// The renewal window slides: a connection that keeps renewing never has
	// to bring the human back, which is what the human approved.
	if err := tx.QueryRow(ctx, `
		INSERT INTO oauth_refresh_token (workspace_id, grant_id, token_hash, expires_at)
		VALUES ($1, $2, $3, now() + $4::interval)
		RETURNING id`,
		l.workspaceID, l.grantID, hashToken(refresh), refreshTokenTTL.String()).Scan(&successorID); err != nil {
		return IssuedPassport{}, "", err
	}
	// The forward link is application-maintained (replaced_by carries no FK)
	// and it is what the replay rule reads: without it a retried token cannot
	// be told from a stolen one.
	if _, err := tx.Exec(ctx,
		`UPDATE oauth_refresh_token SET replaced_by = $2 WHERE id = $1`, l.tokenID, successorID); err != nil {
		return IssuedPassport{}, "", err
	}

	// The predecessor dies before its replacement is minted, so a connector
	// holds exactly one passport and a leaked older access token cannot
	// outlive the renewal that replaced it.
	if err := revokeGrantPassportsTx(ctx, tx, l.grantID); err != nil {
		return IssuedPassport{}, "", err
	}
	label := oauthPassportLabel(l.clientID)
	issued, err := mintPassport(ctx, tx, l.identity(),
		IssuePassportInput{Label: &label, Scopes: scopes}, &l.grantID)
	if err != nil {
		return IssuedPassport{}, "", err
	}
	return issued, refresh, nil
}
