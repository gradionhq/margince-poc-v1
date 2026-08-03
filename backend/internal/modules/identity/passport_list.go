// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The Settings list's READ MODEL, split out of passport.go when it outgrew the
// file cap. It is a different concept from the credential lifecycle next door:
// minting and revoking act on one passport, while this answers "what does this
// human have?" — and the answer is not one row per passport, because a
// connection replaces its credential on every renewal.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// PassportRow is one passport's metadata for the Settings list. The
// token hash never leaves the store — the plaintext existed exactly
// once, in the mint response.
type PassportRow struct {
	ID         ids.PassportID
	Label      *string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	Connection *PassportConnectionRow
}

// PassportConnectionRow is the connection a grant-bound passport belongs to.
// Present exactly when the passport was issued BY the token exchange rather
// than minted by a human, which is the distinction the Settings list is built
// on — a human lends a passport and the client receives one of these.
//
// ClientName falls back to ClientID when the registration is gone: a
// connection whose client was deleted still has to be nameable, because it is
// still live authority somebody may want to end.
type PassportConnectionRow struct {
	ClientID    string
	ClientName  string
	ConnectedAt time.Time
	// Renewable is the grant's refresh_allowed, and it is what the passport's
	// own expires_at means nothing without: a renewable connection that passes
	// that moment is between credentials, a non-renewable one is over. Reading
	// the expiry alone reports the first kind as dead.
	Renewable bool
	// LentPassport is the passport the human lent to create this connection,
	// and its label as it reads now. Both nil for a connection older than that
	// provenance, or one whose lent passport was deleted outright — the lend is
	// history at this point, never a live link, so its absence costs a display
	// line and nothing more.
	LentPassportID    *ids.PassportID
	LentPassportLabel *string
}

// listPassportsSQL enumerates passports as metadata, ONE ROW PER CONNECTION.
//
// The grouping is what makes the list readable over time. Every refresh
// revokes a connection's passport and mints its replacement under the same
// grant (oauth_refresh.go), so an un-grouped list grows a dead row per renewal
// — a connector on the default lifetime buries the human's own passports
// within a day. The newest passport per grant IS the connection, and the
// predecessors are rotation debris the audit log already keeps.
//
// The grouping key is a PAIR, and both halves earn their place. Grouping on
// oauth_grant_id alone is wrong because DISTINCT ON treats NULLs as EQUAL — it
// would fold every human-minted passport in the workspace into one row — so the
// coalesce gives each unbound passport a group of its own. That alone would
// leave two independent uuidv7 namespaces sharing one key, where a passport id
// equal to some grant's id hides one of the two rows. The leading
// `oauth_grant_id IS NULL` separates them: false for every connection, true for
// every minted passport. The two kinds cannot collide at all, rather than
// colliding improbably.
//
// The distinct select is wrapped because its ORDER BY is forced to lead with
// the grouping expression, which is not the order the list is read in. The
// outer ORDER BY restores newest-first, and it stays a total order (id breaks
// the tie on identical timestamps) so paging over it cannot repeat or skip.
//
// %s is the row-scope predicate, never caller data: a user sees their own
// passports, the admin role the workspace's — the same authority split
// RevokePassport enforces.
const listPassportsSQL = `
	SELECT id, label, scopes, created_at, expires_at, revoked_at,
	       client_id, client_name, connected_at, renewable, lent_passport_id, lent_passport_label
	FROM (
		SELECT DISTINCT ON (p.oauth_grant_id IS NULL, COALESCE(p.oauth_grant_id, p.id))
		       p.id, p.label, p.scopes, p.created_at, p.expires_at, p.revoked_at,
		       g.client_id, COALESCE(c.client_name, g.client_id) AS client_name,
		       g.created_at AS connected_at, g.refresh_allowed AS renewable,
		       g.lent_passport_id, lent.label AS lent_passport_label
		FROM passport p
		LEFT JOIN oauth_grant g ON (g.workspace_id, g.id) = (p.workspace_id, p.oauth_grant_id)
		LEFT JOIN oauth_client c ON (c.workspace_id, c.client_id) = (g.workspace_id, g.client_id)
		LEFT JOIN passport lent ON (lent.workspace_id, lent.id) = (g.workspace_id, g.lent_passport_id)
		WHERE %s
		ORDER BY p.oauth_grant_id IS NULL, COALESCE(p.oauth_grant_id, p.id), p.created_at DESC, p.id DESC
	) newest_per_connection
	ORDER BY created_at DESC, id DESC` // #nosec G101 -- a SELECT over passport metadata; it reads no token column

// ListPassports enumerates passports as metadata: a user sees their
// own; the admin role sees the workspace's (the same authority split
// RevokePassport enforces).
func (s *Service) ListPassports(ctx context.Context, id Identity) ([]PassportRow, error) {
	scope, args := "p.on_behalf_of = $1", []any{id.UserID}
	if id.hasRole(roleAdmin) {
		scope, args = "true", nil
	}
	query := fmt.Sprintf(listPassportsSQL, scope)
	var out []PassportRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				p PassportRow
				// The connection columns arrive from a LEFT JOIN, so every one
				// of them is NULL together for a human-minted passport. client
				// id is what decides: it is NOT NULL on a grant, so a NULL
				// there means there was no grant to join, never a grant with a
				// missing client.
				clientID    *string
				clientName  *string
				connectedAt *time.Time
				renewable   *bool
				lentID      *ids.PassportID
				lentLabel   *string
			)
			if err := rows.Scan(&p.ID, &p.Label, &p.Scopes, &p.CreatedAt, &p.ExpiresAt, &p.RevokedAt,
				&clientID, &clientName, &connectedAt, &renewable, &lentID, &lentLabel); err != nil {
				return err
			}
			if clientID != nil && clientName != nil && connectedAt != nil && renewable != nil {
				p.Connection = &PassportConnectionRow{
					ClientID:          *clientID,
					ClientName:        *clientName,
					ConnectedAt:       *connectedAt,
					Renewable:         *renewable,
					LentPassportID:    lentID,
					LentPassportLabel: lentLabel,
				}
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
