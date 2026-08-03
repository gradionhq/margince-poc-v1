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
// another grant for the same client. So a client an operator switches off has
// as many connections beneath it as the human consented to, and each one ends
// through the same cascade below.

import (
	"context"
	"errors"
	"fmt"

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

// errConsentingUserInactive refuses a consent whose human is no longer live.
// It answers on the wire exactly as a spent code does: whether an account
// exists and is deactivated is not something an unauthenticated token request
// may learn.
var errConsentingUserInactive = errors.New("oauth: the consenting user is not active")

// requireLiveConsentingUser refuses to build a connection on authority its
// human no longer has, and takes app_user FOR UPDATE to do it.
//
// The predicate has to be re-read HERE rather than trusted from the
// authorization code. A code minted while the human was live redeems into a
// brand-new grant afterwards, and because a grant outlives every passport
// beneath it, per-call re-auth alone does not cover the gap: the connector is
// merely dormant while the human is deactivated, and reactivating them
// silently restores a connector nobody re-approved.
//
// The lock is what makes it a decision rather than a guess. DeactivateUser
// takes the same app_user row FOR UPDATE before revoking what borrows the
// human's authority, so a redemption racing a deactivation serializes instead
// of interleaving: one of the two goes second and sees the other's outcome.
// Taking it BEFORE the grant insert also keeps the app_user → oauth_grant
// order that cascade already uses, so the two cannot deadlock.
func requireLiveConsentingUser(ctx context.Context, tx pgx.Tx, in issueGrantInput) error {
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status FROM app_user
		WHERE id = $1 AND workspace_id = $2 AND archived_at IS NULL
		FOR UPDATE`, in.UserID, in.WorkspaceID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return errConsentingUserInactive
	}
	if err != nil {
		return err
	}
	if status != userStatusActive {
		return errConsentingUserInactive
	}
	return nil
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
	if err := requireLiveConsentingUser(ctx, tx, in); err != nil {
		return ids.Nil, "", err
	}
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
			auditFieldClientID:       in.ClientID,
			auditFieldScopes:         in.Scopes,
			auditFieldRefreshAllowed: in.RefreshAllowed,
			auditFieldResource:       in.Resource,
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

// reuseRevokeReason is what the audit row says when the cascade was triggered
// by detection rather than by a human.
const reuseRevokeReason = "refresh token reuse detected"

// passportRevokedReason is what the audit row says when a human killed the
// credential and the connection went with it, rather than the other way round.
const passportRevokedReason = "the passport issued under the grant was revoked"

// deactivatedUserRevokeReason is what the audit row says when the connection
// died because the human whose authority it borrows lost their own access.
const deactivatedUserRevokeReason = "the human who consented was deactivated"

// The LOCK ORDER for a connection's rows, obeyed by every path that touches
// more than one of them: oauth_grant first, then oauth_refresh_token, then
// passport. Rotation and revokeGrantTx both take them in that order, so a
// human revoking at the instant a connector renews QUEUES on the grant row
// instead of deadlocking against it. DESIGN §5.4's lock exists to serialize
// every concurrent presentation *and any racing revoke*, and a deadlock —
// Postgres aborting one side with a 500 — is not serialization. A new path
// that takes a refresh row before its grant re-opens exactly that hole.
//
// app_user sits AHEAD of all three: requireLiveConsentingUser locks it before
// the grant insert below, DeactivateUser locks it before revoking the human's own
// passports (users.go), and a consent locks it before the passport it lends
// (lockConsentingUser, oauth_lend.go). A path that takes a passport or refresh row
// and then reaches for app_user inverts that chain and deadlocks against a
// deactivation.
//
// Which is why a path that ALREADY holds the grant must not lock app_user at all:
// it is past app_user's place in the chain, so it reads the human's row unlocked
// and decides from the grant lock it holds (lockPresentedRefreshToken's
// FOR UPDATE OF r, g — oauth_refresh.go states why an MVCC read suffices there).

// lockGrant takes that connection-level lock: the FIRST lock any such path
// acquires. It reads no columns on purpose — a grant's state is read
// authoritatively after this, under the lock. pgx.ErrNoRows passes through so
// each caller answers an absent grant in its own vocabulary.
func lockGrant(ctx context.Context, tx pgx.Tx, grantID ids.UUID) error {
	var locked ids.UUID
	return tx.QueryRow(ctx,
		`SELECT id FROM oauth_grant WHERE id = $1 FOR UPDATE`, grantID).Scan(&locked)
}

// revokeGrantTx ends a whole connection inside the caller's transaction: the
// consent, every refresh token that could renew it, and every passport it
// issued. It is the ONE cascade, and four paths reach these three writes:
// detected refresh-token reuse (rotateRefreshToken), a human deleting a
// passport (RevokePassport), a client revoking its own credential
// (revokeToken, RFC 7009) and an admin deactivating the human whose authority
// the connection borrows (revokeGrantsOfUserTx). So no path can end a
// connection halfway and none can leave refresh able to resurrect it.
//
// A client disabled or deleted OUT OF BAND — there is no admin client surface
// yet, so that is raw SQL today — reaches no cascade at all: what stops it is
// the liveness predicate every read of oauth_client carries (liveClientPredicate,
// oauth.go), which refuses authentication, renewal and fresh consent alike.
//
// The actor must already be bound on ctx (actorCtx): the audit row names
// whose action this was, and storekit refuses an unattributed write rather
// than record an anonymous revocation.
//
// Idempotent — the revocation of an already-revoked grant is audited once and
// re-emits nothing, because every write below is conditional on the row it
// touches still being live.
func (s *Service) revokeGrantTx(ctx context.Context, tx pgx.Tx, grantID ids.UUID, reason string) error {
	// Grant row FIRST, then the refresh rows, then the passports — the lock
	// order stated above, which rotation also takes, so the two paths queue
	// instead of deadlocking. Taking it EXPLICITLY, rather than letting the
	// conditional UPDATE below take it as a side effect, is what lets every
	// caller inherit the order simply by entering here: the UPDATE locks
	// nothing on a grant that is already revoked, and this function still
	// walks the rows beneath it in that case.
	if err := lockGrant(ctx, tx, grantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The FK from passport (and from oauth_refresh_token) to oauth_grant
			// is RESTRICT precisely so a live credential cannot outlive the
			// consent record that authorized it. An absent grant with rows to
			// revoke beneath it is therefore a broken invariant, not a caller
			// mistake, and must not read as "revoked successfully".
			return fmt.Errorf("identity: cannot revoke grant %s: the grant row is absent", grantID)
		}
		return err
	}
	// The conditional UPDATE is the serialization point for the AUDIT: two
	// simultaneous revokes queue on the lock above and only the first sees the
	// grant live, so one revocation is recorded once. The row walks below are
	// idempotent on their own row state, so a second revoke arriving from
	// another direction re-checks them and finds nothing left to do.
	tag, err := tx.Exec(ctx,
		`UPDATE oauth_grant SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, grantID)
	if err != nil {
		return err
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
	if tag.RowsAffected() == 0 {
		return nil // already revoked: the rows beneath it were re-checked, the fact is on record
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

// revokeGrantsOfUserTx ends every connection one human consented to, through
// the ONE cascade, inside the caller's transaction. It is what a path that ends
// a HUMAN's access calls: revoking their passports alone is not enough,
// because the grant beneath one can mint a replacement on the connector's next
// renewal and each renewal slides the refresh window forward, so a connection
// nobody is watching outlives the access it borrows.
//
// It enumerates the GRANTS, not the passports beneath them: a grant whose
// passports are all already revoked still has a spendable refresh chain, so
// "this human holds no live passport" is not "this human has no live
// connection". Ordering by id makes the lock sequence deterministic when a
// human consented more than once.
func (s *Service) revokeGrantsOfUserTx(ctx context.Context, tx pgx.Tx, userID ids.UserID, reason string) error {
	rows, err := tx.Query(ctx,
		`SELECT id FROM oauth_grant WHERE user_id = $1 AND revoked_at IS NULL ORDER BY id`, userID)
	if err != nil {
		return err
	}
	// Collected before the first write: the cascade below runs statements on
	// this same connection, which an open row set would be reading from.
	grantIDs, err := pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
	if err != nil {
		return err
	}
	for _, grantID := range grantIDs {
		if err := s.revokeGrantTx(ctx, tx, grantID, reason); err != nil {
			return err
		}
	}
	return nil
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
		if err := auditPassportRevoked(ctx, tx, passportID, by); err != nil {
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

// revokeTokenInput is a presented token as RFC 7009 places it on the wire:
// the token itself and an optional hint at which kind it is.
type revokeTokenInput struct {
	Token         string
	TokenTypeHint string
}

// clientRevokeReason is what the audit row says when the CLIENT ended the
// connection from its own side (RFC 7009) — distinct from a human deleting a
// passport in Settings (passportRevokedReason) and from detected
// refresh-token reuse (reuseRevokeReason).
const clientRevokeReason = "revoked via RFC 7009 /oauth/revoke"

// revokeToken is RFC 7009 revocation: whichever half of a connection's
// credential pair was presented, the whole connection dies through the ONE
// cascade. Resolution is unlocked exactly like grantOfPresentedToken — it
// names which grant to lock and decides nothing else, so revokeGrantTx alone
// takes the grant → refresh → passport order (oauth_grant.go) and no read
// here can invert it by locking a passport or refresh row first.
//
// An unresolved token — unknown, from another workspace, or naming no grant
// at all (a locally minted passport answers to no connection) — commits
// nothing and reports success exactly like a genuine revocation: RFC 7009
// forbids this endpoint from ever becoming an oracle for whether a token
// string is real.
func (s *Service) revokeToken(ctx context.Context, in revokeTokenInput) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		grantID, err := resolveGrantID(ctx, tx, in)
		if err != nil {
			return err
		}
		if grantID == ids.Nil {
			return nil
		}
		var userID ids.UserID
		if err := tx.QueryRow(ctx, `SELECT user_id FROM oauth_grant WHERE id = $1`, grantID).
			Scan(&userID); err != nil {
			return err
		}
		// The human attributed is the one who consented, not whoever
		// presented the token — there is no session on this call, exactly as
		// a refresh rotation has none (lockedGrant.identity()).
		return s.revokeGrantTx(actorCtx(ctx, Identity{UserID: userID}), tx, grantID, clientRevokeReason)
	})
}

// resolveGrantID names which grant a presented token belongs to, checking
// passport and oauth_refresh_token by hash — the two tables a client may hand
// back either half of a connection's credential pair from. token_type_hint
// only orders which table is tried first: a miss on the hinted table still
// falls through to the other, so a wrong hint never turns into a refusal
// (RFC 7009 §2.1 — the hint "SHOULD" be honored, but a server "MUST NOT" rely
// on it as authoritative).
func resolveGrantID(ctx context.Context, tx pgx.Tx, in revokeTokenInput) (ids.UUID, error) {
	hash := hashToken(in.Token)
	if in.TokenTypeHint == oauthRefreshToken {
		if grantID, err := refreshGrantID(ctx, tx, hash); err != nil || grantID != ids.Nil {
			return grantID, err
		}
		return passportGrantID(ctx, tx, hash)
	}
	if grantID, err := passportGrantID(ctx, tx, hash); err != nil || grantID != ids.Nil {
		return grantID, err
	}
	return refreshGrantID(ctx, tx, hash)
}

// passportGrantID resolves a presented access token to the grant it was
// issued under. A locally minted passport — one with no OAuth grant beneath
// it — reports no grant: this endpoint's one cascade ends a CONNECTION, and a
// passport with no connection to revoke is, for this purpose, indistinguishable
// from an unknown token.
func passportGrantID(ctx context.Context, tx pgx.Tx, hash string) (ids.UUID, error) {
	var grantID *ids.UUID
	err := tx.QueryRow(ctx, `SELECT oauth_grant_id FROM passport WHERE token_hash = $1`, hash).
		Scan(&grantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, nil
	}
	if err != nil {
		return ids.Nil, err
	}
	if grantID == nil {
		return ids.Nil, nil
	}
	return *grantID, nil
}

// refreshGrantID resolves a presented refresh token to the grant it renews.
// Unlocked for the same reason grantOfPresentedToken is (oauth_refresh.go):
// naming the grant is all a resolution read may decide — the lock itself is
// revokeGrantTx's, taken in the one order this connection's rows obey.
func refreshGrantID(ctx context.Context, tx pgx.Tx, hash string) (ids.UUID, error) {
	var grantID ids.UUID
	err := tx.QueryRow(ctx, `SELECT grant_id FROM oauth_refresh_token WHERE token_hash = $1`, hash).
		Scan(&grantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, nil
	}
	return grantID, err
}
