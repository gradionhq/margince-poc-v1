// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Running the LinkedIn matcher under each ghost owner's OWN authority
// (ADR-0078 §2.1b).
//
// The matcher decides which of a member's imported connections is which
// contact, and a match is reported back to that member. So the question it
// answers is not "does this contact exist" but "can THIS member see this
// contact" — and the answer has to come from that member's live row scope.
//
// The background passes (the event consumer and the hourly sweep) have no
// human, so they used to run as a SYSTEM principal. A system principal is
// unbounded by design — provisioning, the relay and the privacy engines have to
// read every row — which made auth.ScopeClauseFor return an empty clause and
// left the match unrestricted. That turned a one-row CSV into an oracle: upload
// a guessed address, wait for the sweep, and read match_status to learn whether
// a contact with that address exists somewhere the member cannot reach.
//
// So the passes enumerate the OWNERS with ghosts and run once per owner under
// that owner's resolved authority. It costs one RBAC read per member with an
// import — a handful per workspace — and it is the only shape in which the
// answer is the same whether a human or a timer asked.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/authz"
)

// ghostOwners lists the members of the bound workspace who have imported
// connections that are still undecided.
//
// Read under whatever principal the caller holds — it names members, not
// records, and the roster is readable by any authenticated member. Only
// undecided ghosts count, because a workspace whose queue is clear should cost
// one query rather than one query per member.
func ghostOwners(ctx context.Context, pool *pgxpool.Pool) ([]ids.UUID, error) {
	var out []ids.UUID
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT owner_user_id FROM linkedin_connection
			 WHERE match_status = 'unmatched' AND tombstoned_at IS NULL`)
		if err != nil {
			return fmt.Errorf("compose: listing the members with ghosts to match: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// asGhostOwner binds one member's live authority to the context.
//
// The principal is a HUMAN acting on their own behalf, not a system principal
// wearing their id: the whole point is that auth.ScopeClauseFor renders their
// real own/team/all clause and auth.UnboundedFor applies capture privacy to
// them. A member who has since been archived, suspended or removed resolves to
// ErrNotFound and their ghosts are simply not swept — absence of authority is
// denial, never empty permission.
func asGhostOwner(ctx context.Context, resolver authz.Resolver, workspace, owner ids.UUID) (context.Context, error) {
	rbac, err := resolver.EffectiveRBAC(ctx, workspace, owner)
	if err != nil {
		return nil, err
	}
	return principal.WithActor(ctx, principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "user:" + owner.String(),
		UserID:      owner,
		OnBehalfOf:  owner,
		Permissions: rbac.Permissions,
		TeamIDs:     rbac.TeamIDs,
	}), nil
}

// forEachGhostOwner runs one pass per member with undecided ghosts.
//
// A member whose authority cannot be resolved — archived, suspended, or simply
// holding no person grant — is SKIPPED, not fatal. One departed colleague, or
// one seat that reads no people, must not stop every other member's network
// being matched. Any other error stops the pass, because it is not a statement
// about one member.
func forEachGhostOwner(ctx context.Context, pool *pgxpool.Pool, resolver authz.Resolver, workspace ids.UUID,
	run func(ownerCtx context.Context, owner ids.UUID) error,
) error {
	owners, err := ghostOwners(ctx, pool)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		ownerCtx, err := asGhostOwner(ctx, resolver, workspace, owner)
		if err != nil {
			if errors.Is(err, apperrors.ErrNotFound) {
				continue
			}
			return err
		}
		if err := run(ownerCtx, owner); err != nil {
			if errors.Is(err, apperrors.ErrPermissionDenied) {
				continue
			}
			return err
		}
	}
	return nil
}
