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

	"github.com/jackc/pgx/v5"

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
