// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The durable record of what a human approved: one oauth_grant row per
// consent, and the rotating refresh tokens minted beneath it. Without it a
// connector's whole authority lived in a passport that expired with nothing
// able to renew it, and the client, the human, and the audience the consent
// covered were recoverable only from the passport's label.
//
// A grant is one act of consent, not one connection: re-consenting mints
// another grant for the same client, so what ends a connection is disabling
// the CLIENT and cascading to every grant beneath it.

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// refreshTokenPrefix tags a refresh token the way passportTokenPrefix tags a
// passport. Both are the same 32-byte base64url shape hashed into different
// tables, so without a tag a leaked string names neither its kind nor the
// table that would revoke it.
const refreshTokenPrefix = "mgr_"

// refreshTokenTTL is how long a connection may keep renewing itself before
// the human has to consent again. It is maxPassportTTL rather than a number
// of its own: the window in which a connector renews with no human in the
// loop must not exceed the longest single authority a human can grant in
// one act.
const refreshTokenTTL = maxPassportTTL

// issueGrantInput is the consent as approved: the client it was approved
// for, the passport scopes the credentials under it carry, whether
// offline_access rode the request (the only thing that makes refresh
// possible at all), and the RFC 8707 audience the authorization was bound
// to — nil for a client that named none, which is exactly what the code row
// recorded and must not be upgraded to a binding the client never asked for.
type issueGrantInput struct {
	WorkspaceID    ids.WorkspaceID
	UserID         ids.UserID
	ClientID       string
	Scopes         []string
	RefreshAllowed bool
	Resource       *string
}

// issueGrant records one approved consent and mints the first refresh token
// beneath it inside the CALLER's transaction, so the grant commits together
// with the authorization-code consumption that authorized it and the
// passport that follows: a client holding a refresh token for a grant that
// does not exist, or a passport with no grant to revoke it through, are
// states this flow cannot reach.
//
// The refresh plaintext is returned exactly once and only its hash is
// stored. It is empty when the grant does not allow refresh — then there is
// no credential to hand back.
func issueGrant(ctx context.Context, tx pgx.Tx, in issueGrantInput) (grantID ids.UUID, refresh string, err error) {
	err = tx.QueryRow(ctx, `
		INSERT INTO oauth_grant (workspace_id, client_id, user_id, scopes, refresh_allowed, resource)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		in.WorkspaceID, in.ClientID, in.UserID, in.Scopes, in.RefreshAllowed, in.Resource).Scan(&grantID)
	if err != nil {
		return ids.Nil, "", err
	}
	// Granting a remote client standing, renewable authority over the human's
	// own records is audited as its own fact, separate from the passport
	// minted under it: the consent outlives every passport it issues and is
	// what an admin later disables.
	auditCtx := actorCtx(ctx, Identity{UserID: in.UserID, WorkspaceID: in.WorkspaceID})
	if _, err := storekit.Audit(auditCtx, tx, "create", "oauth_grant", grantID, nil,
		map[string]any{
			"client_id":       in.ClientID,
			"scopes":          in.Scopes,
			"refresh_allowed": in.RefreshAllowed,
			"resource":        in.Resource,
		}); err != nil {
		return ids.Nil, "", err
	}
	if !in.RefreshAllowed {
		return grantID, "", nil
	}

	raw, err := randomToken()
	if err != nil {
		return ids.Nil, "", err
	}
	// The stored hash covers the PREFIXED token, exactly as a passport's
	// does, so there is one token spelling and the lookup hashes what the
	// wire carried.
	refresh = refreshTokenPrefix + raw
	// replaced_by stays NULL: the first token in a chain succeeds nothing,
	// and rotation is what fills the forward link.
	if _, err := tx.Exec(ctx, `
		INSERT INTO oauth_refresh_token (workspace_id, grant_id, token_hash, expires_at)
		VALUES ($1, $2, $3, now() + $4::interval)`,
		in.WorkspaceID, grantID, hashToken(refresh), refreshTokenTTL.String()); err != nil {
		return ids.Nil, "", err
	}
	return grantID, refresh, nil
}

// oauthPassportLabel names the client a passport was issued for, so Settings
// shows which connection a credential belongs to. Spelled once because the
// code exchange and every later rotation must produce the same label — a
// renewal that relabelled the connection would read as a second connector.
func oauthPassportLabel(clientID string) string { return "oauth:" + clientID }

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

// reuseRevokeReason is what the audit row says when the cascade was triggered
// by detection rather than by a human.
const reuseRevokeReason = "refresh token reuse detected"

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
// transaction that opens by locking the token AND its grant: every concurrent
// presentation of the same token queues behind the winner and sees it already
// consumed, and a racing revoke cannot interleave with the reissue. A
// read-then-write here would mint a successor per presentation, leaving a
// connector holding divergent chains.
func (s *Service) rotateRefreshToken(ctx context.Context, in refreshRequest) (IssuedPassport, string, error) {
	var (
		issued  IssuedPassport
		refresh string
		reused  bool
	)
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		locked, err := lockPresentedRefreshToken(ctx, tx, hashToken(in.Token))
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

// lockPresentedRefreshToken reads the presented token, its grant and its
// client, and holds a write lock on the first two for the rest of the
// transaction — the serialization point the whole rotation rests on.
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
	if !audienceMatches(in.Resource, in.CanonicalResource, l.resource) {
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

// revokeGrantTx ends a whole connection inside the caller's transaction: the
// consent, every refresh token that could renew it, and every passport it
// issued. It is the ONE cascade — detected reuse, an admin disabling or
// deleting a client, a human deleting a passport and RFC 7009 revocation all
// reach these three writes, so no path can end a connection halfway and no
// path can leave refresh able to resurrect it.
//
// The actor must already be bound on ctx (actorCtx): the audit row names
// whose action this was, and storekit refuses an unattributed write rather
// than record an anonymous revocation.
//
// Idempotent — an already-revoked grant is a no-op, so a second revoke
// arriving from another direction neither double-audits nor re-emits.
func (s *Service) revokeGrantTx(ctx context.Context, tx pgx.Tx, grantID ids.UUID, reason string) error {
	// The conditional UPDATE is also the serialization point for callers that
	// do not already hold the grant row's lock: two simultaneous revokes
	// cannot both see it live.
	tag, err := tx.Exec(ctx,
		`UPDATE oauth_grant SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, grantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	// A refresh row has no revoked_at of its own: consumed_at IS the spend
	// marker, and a token whose grant is dead is refused on the liveness
	// check before the replay rule ever reads it — so marking the chain spent
	// closes renewal for good without a second column meaning the same thing.
	if _, err := tx.Exec(ctx,
		`UPDATE oauth_refresh_token SET consumed_at = now() WHERE grant_id = $1 AND consumed_at IS NULL`,
		grantID); err != nil {
		return err
	}
	if err := revokeGrantPassportsTx(ctx, tx, grantID); err != nil {
		return err
	}
	// The reason is evidence ABOUT the revocation, not a field of the grant,
	// so it rides evidence rather than the after image. The closed catalog
	// (events.md §5) defines no oauth_grant.* verb, so the passport.revoked
	// events above are the bus-visible half of this cascade — a consumer
	// holding a credential learns it died; the missing grant-level type is
	// raised upstream (P3).
	_, err = storekit.AuditWithEvidence(ctx, tx, "archive", "oauth_grant", grantID, nil, nil,
		map[string]any{"reason": reason})
	return err
}

// revokeGrantPassportsTx kills every live passport under a grant and puts
// each death on the bus. Rotation uses it to retire the predecessor and the
// cascade uses it to end the connection, so "the credentials under this
// consent stop working" has one spelling. The event is per passport because a
// long-lived holder has to drop THAT credential — not learn that some
// connection changed.
func revokeGrantPassportsTx(ctx context.Context, tx pgx.Tx, grantID ids.UUID) error {
	by, err := revokingUser(ctx)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx,
		`UPDATE passport SET revoked_at = now()
		 WHERE oauth_grant_id = $1 AND revoked_at IS NULL
		 RETURNING id`, grantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var revoked []ids.PassportID
	for rows.Next() {
		var passportID ids.PassportID
		if err := rows.Scan(&passportID); err != nil {
			return err
		}
		revoked = append(revoked, passportID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// The audit + event rows are written after the walk, not inside it: the
	// connection is busy while rows are being read.
	for _, passportID := range revoked {
		auditID, err := storekit.Audit(ctx, tx, "archive", "passport", passportID.UUID, nil, nil)
		if err != nil {
			return err
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, passportID.UUID,
			passportRevokedPayload(passportID, by)); err != nil {
			return err
		}
	}
	return nil
}

// revokingUser reads the human a cascade is attributed to back off the
// context, so the passport.revoked payload names them — the same principal
// the audit row is stamped from, never a second guess at who acted.
func revokingUser(ctx context.Context) (ids.UserID, error) {
	actor, err := storekit.Actor(ctx)
	if err != nil {
		return ids.UserID{}, err
	}
	return ids.From[ids.UserKind](actor.UserID), nil
}
