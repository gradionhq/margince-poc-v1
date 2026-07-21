// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// SeedUserMap matches the incumbent's owners directory against the
// workspace's own app_user rows and writes one email-sourced
// mirror_user_map row per owner whose email equals an existing user's
// (case/whitespace normalized both sides) — the design.md §4.6 "match,
// never import" rule that turns a just-connected overlay from serving
// nobody (nothing writes mirror_user_map otherwise) into serving exactly
// the users the incumbent actually owns records through. An owner with an
// empty email, or one no workspace user matches, is skipped — never
// guessed (fail-closed). Each matched pairing goes through UpsertUserMap,
// so it inherits that path's re-verification against the incumbent's
// current owner email and its atomic clear-then-grant visibility
// recompute (including the remap-revoke when a user was already mapped to
// a different owner). Per-owner failures are collected, not fatal: one
// owner whose email no longer resolves (a race between the directory pull
// and the re-check) must not stop the rest from seeding, so the errors
// are joined and returned for the caller to log while every seedable
// owner still lands.
//
// Cost: one app_user lookup per distinct-email owner per sweep — bounded
// by the owners-directory size (tens to low hundreds), not the record
// count, so it stays cheap at the scale this runs.
func (s *MirrorStore) SeedUserMap(ctx context.Context, incumbent string, owners []OwnerRef) error {
	// Ambiguity guard (design.md §4.6: "zero OR ambiguous match
	// → no row"). HubSpot allows two owners to carry the same email
	// (a deactivated owner recreated under a new id), so group owners by
	// normalized email and drop any email more than one owner claims:
	// seeding a user to "whichever owner the directory listed last" would
	// be a nondeterministic remap that revokes the prior owner's records
	// every sweep. Only an email owned by exactly one owner is seedable.
	byEmail := make(map[string]OwnerRef)
	ambiguous := make(map[string]bool)
	for _, owner := range owners {
		email := normalizeEmail(owner.Email)
		if owner.ExternalID == "" || email == "" {
			continue
		}
		if _, seen := byEmail[email]; seen {
			ambiguous[email] = true
			continue
		}
		byEmail[email] = owner
	}

	var errs []error
	for email, owner := range byEmail {
		if ambiguous[email] {
			continue
		}
		users, err := s.usersMatchingEmail(ctx, owner.Email, incumbent)
		if err != nil {
			errs = append(errs, fmt.Errorf("overlay: matching users for owner %s: %w", owner.ExternalID, err))
			continue
		}
		for _, appUser := range users {
			if err := s.UpsertUserMap(ctx, appUser, incumbent, owner.ExternalID, "email"); err != nil {
				errs = append(errs, fmt.Errorf("overlay: seeding %s to owner %s: %w", appUser, owner.ExternalID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// usersMatchingEmail lists the workspace app_user ids whose email equals
// email (case/whitespace normalized both sides) AND who do NOT already
// carry a match_source='manual' mapping for this incumbent — the candidate
// set SeedUserMap pairs one incumbent owner against. Excluding manual rows
// here is the escape-hatch guarantee (design.md §4.6 rule 4, the same rule
// revalidateEmailMapping honors): an admin's manual override must be
// sticky against the sweep automation it exists to escape, so seeding
// never clobbers it (upsertUserMapSQL's ON CONFLICT would otherwise
// overwrite incumbent_user_id AND match_source unconditionally). It runs
// under a workspace-scoped tx so RLS confines the match to the connected
// workspace's own users; a directory owner whose email belongs to a user
// in some OTHER tenant can never leak a cross-workspace mapping.
func (s *MirrorStore) usersMatchingEmail(ctx context.Context, email, incumbent string) ([]ids.UserID, error) {
	var users []ids.UserID
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT u.id FROM app_user u
			WHERE lower(trim(u.email)) = lower(trim($1))
			  AND NOT EXISTS (
			      SELECT 1 FROM mirror_user_map m
			      WHERE m.app_user_id = u.id AND m.incumbent = $2 AND m.match_source = 'manual'
			  )`, email, incumbent)
		if err != nil {
			return fmt.Errorf("overlay: querying users by email: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id ids.UserID
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("overlay: scanning a matched user id: %w", err)
			}
			users = append(users, id)
		}
		return rows.Err()
	})
	return users, err
}
