// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file owns email-sourced mirror_user_map revalidation: split out of
// visibility.go (which keeps the deny-join, seeding, and the ambiguity
// primitives it builds on) to stay under the file-length cap — a mechanical
// relocation, no change to the logic itself.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// revalidateEmailMapping re-verifies every email-sourced mirror_user_map
// row pointing at incumbentUserID against its CURRENT owner email (read
// through emails — Ingest passes s.emails, RevalidateEmailMappings passes
// whatever live incumbent adapter its caller resolved for this
// workspace), deletes any that no longer match, and — in the SAME
// transaction — recomputes incumbentUserID's visibility projections.
// Deleting a stale mapping row without that recompute would leave the
// de-mapped user's mirror_visibility rows exactly as they were: reads are
// gated on can_see, not on a live mirror_user_map join (mirrorstore.go's
// visibilityJoin), so a dangling can_see=true row keeps granting access
// forever after the mapping that justified it is gone. Because
// ProjectOwnerVisibility clears-then-grants from the mapping table's
// CURRENT contents, recomputing after the delete is what actually drops
// the removed user's grant, not merely tidying the mapping table.
//
// Two call sites, two purposes: Ingest calls this when a record's
// owner_external_id changes to incumbentUserID — the only signal
// available at ingest time (Record carries an owner id, never an owner
// email) that this incumbent user is newly relevant and its
// email-derived mapping deserves a fresh look. RevalidateEmailMappings
// calls it periodically for every email-sourced owner regardless of
// whether any record was just reassigned, closing the gap where an
// owner's email changes with their record ownership staying put — per
// design.md §4.6 rule 5: "the mapping is re-validated when the incumbent
// user's email changes … dropping to fail-closed until re-matched or
// manually overridden." A manual (match_source="manual") row is a human
// override and is never touched here — only "email" rows are
// re-verified.
func (s *MirrorStore) revalidateEmailMapping(ctx context.Context, tx pgx.Tx, emails OwnerEmailResolver, incumbentUserID string) error {
	currentEmail, err := emails.OwnerEmail(ctx, incumbentUserID)
	if err != nil {
		// Unresolvable ⇒ cannot confirm the mapping is still correct;
		// fail closed by treating it as no email at all (matches nothing
		// below, so every email-sourced row for this owner is dropped).
		currentEmail = ""
	}
	tag, err := tx.Exec(ctx, `
		DELETE FROM mirror_user_map m
		WHERE m.incumbent_user_id = $1
		  AND m.match_source = 'email'
		  AND NOT EXISTS (
		      SELECT 1 FROM app_user u
		      WHERE u.workspace_id = m.workspace_id AND u.id = m.app_user_id
		        AND lower(trim(u.email)) = lower(trim($2))
		  )`, incumbentUserID, currentEmail)
	if err != nil {
		return fmt.Errorf("overlay: revalidating the email-sourced mapping for %s: %w", incumbentUserID, err)
	}
	// Recompute only when a mapping was actually dropped. A no-op
	// revalidation (the common case — the email still matches) would
	// otherwise rewrite every visibility row for this owner on each pass,
	// making an initial backfill quadratic in an owner's record count.
	if tag.RowsAffected() == 0 {
		return nil
	}
	return recomputeForOwnerTx(ctx, tx, incumbentUserID)
}

// distinctEmailSourcedOwnersSQL lists every incumbent_user_id this
// workspace has AT LEAST ONE email-sourced (never manual) mirror_user_map
// row for — the bounded population RevalidateEmailMappings re-checks each
// pass, rather than scanning every mapping row per owner redundantly.
const distinctEmailSourcedOwnersSQL = `
SELECT DISTINCT incumbent_user_id FROM mirror_user_map WHERE match_source = 'email'`

// RevalidateEmailMappings re-verifies EVERY email-sourced mirror_user_map
// row in the current workspace against emails' current owner email,
// dropping (and revoking the visibility of) any that no longer match —
// the periodic realization of design.md §4.6 rule 5 for the case Ingest's
// own reassignment-triggered revalidateEmailMapping call cannot reach: an
// incumbent owner whose email changes while their record ownership stays
// exactly as it was. Intended to be run once per reconcile sweep per
// workspace connection (compose/jobs_overlay.go's reconcileConnection), with
// emails bound to that sweep's own live incumbent adapter so the email
// this checks against is the incumbent's current value, not a stale one
// resolved at MirrorStore construction time.
//
// Fenced per owner, same as UpsertUserMap/ingestTx (assertFence before
// lockWorkspaceVisibility): revalidateEmailMapping deletes mirror_user_map
// rows and, through recomputeForOwnerTx, both revokes AND re-grants
// mirror_visibility — a sweep straddling a disconnect+reconnect must not
// run this against a directory that no longer belongs to the active
// connection, the same hazard revokeEmailMappingsForOwners is fenced
// against (usermapseed.go). A revoked owner's ErrConnectionGone stops the
// whole pass rather than continuing to churn owners that will all hit the
// same fence — reconcileConnection (jobs_overlay.go) treats it as the same
// clean stop every other fenced sweep write does.
func (s *MirrorStore) RevalidateEmailMappings(ctx context.Context, emails OwnerEmailResolver) error {
	owners, err := s.listEmailSourcedOwners(ctx)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if err := s.revalidateOneOwner(ctx, emails, owner); err != nil {
			if errors.Is(err, ErrConnectionGone) {
				return err
			}
			return fmt.Errorf("overlay: revalidating the email-sourced mapping for owner %s: %w", owner, err)
		}
	}
	return nil
}

// listEmailSourcedOwners answers the distinct incumbent_user_ids this
// workspace has at least one email-sourced mirror_user_map row for — the
// bounded population RevalidateEmailMappings re-checks each pass.
func (s *MirrorStore) listEmailSourcedOwners(ctx context.Context) ([]string, error) {
	var owners []string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, distinctEmailSourcedOwnersSQL)
		if err != nil {
			return fmt.Errorf("overlay: listing email-sourced owners to revalidate: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var owner string
			if err := rows.Scan(&owner); err != nil {
				return fmt.Errorf("overlay: scanning an email-sourced owner to revalidate: %w", err)
			}
			owners = append(owners, owner)
		}
		return rows.Err()
	})
	return owners, err
}

// revalidateOneOwner is RevalidateEmailMappings' per-owner transaction:
// fenced (assertFence before lockWorkspaceVisibility, the same order every
// fenced visibility mutator takes) then revalidateEmailMapping's own
// delete-if-stale-then-recompute.
func (s *MirrorStore) revalidateOneOwner(ctx context.Context, emails OwnerEmailResolver, owner string) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := s.assertFence(ctx, tx); err != nil {
			return err
		}
		if err := lockWorkspaceVisibility(ctx, tx); err != nil {
			return err
		}
		return s.revalidateEmailMapping(ctx, tx, emails, owner)
	})
}
