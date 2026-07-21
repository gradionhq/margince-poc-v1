// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// OwnerEmailResolver is the narrow slice of Incumbent MirrorStore needs for
// visibility: resolving an incumbent-side owner reference to its current
// email, so an email-sourced mirror_user_map row can be verified (and
// re-verified) rather than trusted forever. Both overlay/hubspot.Adapter
// and overlay/fake.Adapter already implement Incumbent's OwnerEmail method
// with this exact shape, so they satisfy this interface with no change —
// MirrorStore asks for only what it uses instead of the full Incumbent
// surface (Backfill/Modified/Get/Associations belong to the mirror engine,
// not to visibility).
type OwnerEmailResolver interface {
	OwnerEmail(ctx context.Context, ownerExternalID string) (string, error)
}

// visibilityJoin is the deny-projection Get/List add to every mirror read
// (design.md §4.6: "joined on every overlay read"; can_see=false or no
// entry ⇒ row not returned). It always binds mirrorUserID as the query's
// FIRST positional parameter ($1) — callers append their own params after
// it, never before, so the placeholder numbering here never collides with
// theirs.
func visibilityJoin(mirrorUserID ids.UUID) (clause string, args []any) {
	return `JOIN mirror_visibility v
       ON v.workspace_id = m.workspace_id
      AND v.object_class = m.object_class
      AND v.external_id = m.external_id
      AND v.mirror_user_id = $1
      AND v.can_see`, []any{mirrorUserID}
}

// resolveActingMirrorUserID looks up the ctx principal's mirror_user_map
// row and returns their own app_user id as the "acting" mirror_user_id
// the deny-join filters on (mirror_visibility.mirror_user_id references
// app_user, not the incumbent-side user — design.md §4.6's "owner/app-scope
// projection"). NO map row is a fail-closed unmapped user: it answers
// apperrors.ErrNotFound (existence-hiding), never ErrPermissionDenied —
// this is a ROW-scope miss, not an object-level RBAC denial.
func resolveActingMirrorUserID(ctx context.Context, tx pgx.Tx) (ids.UUID, error) {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return ids.UUID{}, errors.New("overlay: no principal bound to context")
	}
	var mapped ids.UUID
	err := tx.QueryRow(ctx,
		`SELECT app_user_id FROM mirror_user_map WHERE app_user_id = $1 LIMIT 1`,
		actor.UserID,
	).Scan(&mapped)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ids.UUID{}, apperrors.ErrNotFound
		}
		return ids.UUID{}, fmt.Errorf("overlay: resolving the acting mirror user: %w", err)
	}
	return mapped, nil
}

const clearVisibilitySQL = `
DELETE FROM mirror_visibility
WHERE object_class = $1 AND external_id = $2`

// grantVisibilitySQL grants can_see=true to every app_user currently
// mapped (any incumbent, any match_source) to ownerExternalID — shared-seat
// (many app users → one incumbent user) is allowed by design.md §4.6 rule
// so every mapped app user, not just one, is projected visible.
const grantVisibilitySQL = `
INSERT INTO mirror_visibility (workspace_id, incumbent, mirror_user_id, object_class, external_id, can_see, snapshot_at)
SELECT NULLIF(current_setting('app.workspace_id',true),'')::uuid, m.incumbent, m.app_user_id, $2, $3, true, now()
FROM mirror_user_map m
WHERE m.incumbent_user_id = $1
ON CONFLICT (workspace_id, incumbent, mirror_user_id, object_class, external_id)
DO UPDATE SET can_see = true, snapshot_at = now()`

// ProjectOwnerVisibility computes can_see = (owner maps to this user) for
// one mirror record and writes it to mirror_visibility — the "trivial
// refresher" design.md §4.6 requires to be computed INLINE in the ingest
// upsert tx, never as a trailing backfill-completion pass (that would hide
// the whole portal until backfill finishes, the empty-CRM bug the inline
// placement exists to avoid). It clears any prior projection for this
// record first, so an owner reassignment (or an owner going from mapped to
// unmapped) never leaves a stale can_see=true row for the old owner's
// mapped users.
func ProjectOwnerVisibility(ctx context.Context, tx pgx.Tx, objectClass, externalID, ownerExternalID string) error {
	if objectClass == "" || externalID == "" {
		return fmt.Errorf("overlay: projecting visibility requires a non-empty object class and external id")
	}
	if _, err := tx.Exec(ctx, clearVisibilitySQL, objectClass, externalID); err != nil {
		return fmt.Errorf("overlay: clearing stale visibility for %s/%s: %w", objectClass, externalID, err)
	}

	if ownerExternalID == "" {
		// NULL-OWNER RULE (pinned decision, design.md §4.6): "an unowned
		// HubSpot record is neither silently hidden from all nor silently
		// leaked to all — decide and pin one rule (default:
		// workspace-visible only if HubSpot's own default makes it so,
		// else hidden)." This build has no read on HubSpot's own
		// default-sharing setting (no such field crosses the Incumbent
		// seam today), so the "workspace-visible if HubSpot's default
		// makes it so" half is not something we can honor without
		// guessing — and guessing risks a leak. We take the "else
		// hidden" half: no mirror_visibility row is written for anyone,
		// which the deny-join's "no entry ⇒ hidden" rule turns into
		// fail-closed hidden for every user until the record is
		// assigned an owner. This is the security-conservative choice
		// where the rule is genuinely left open.
		return nil
	}

	if _, err := tx.Exec(ctx, grantVisibilitySQL, ownerExternalID, objectClass, externalID); err != nil {
		return fmt.Errorf("overlay: projecting owner visibility for %s/%s: %w", objectClass, externalID, err)
	}
	return nil
}

// recomputeForOwnerTx re-projects visibility for every already-mirrored
// record owned by incumbentUserID, inside a caller-supplied transaction —
// the recompute trigger design.md §4.6 requires on a mirror_user_map
// change: "the map is incomplete at backfill time; a row whose owner is
// unmapped then fails-closed (correct) but must be recomputed when the
// mapping row is later added, else it stays hidden until the branch-1b
// bulk refresh with no branch-1 remedy." Because ProjectOwnerVisibility's
// clear-then-grant re-derives can_see from the CURRENT mirror_user_map
// contents (not just adds to it), running this against an owner a user
// was JUST unmapped from also revokes that user's stale grants — the same
// primitive both directions of a remap need, so UpsertUserMap and
// revalidateEmailMapping both call it, always inside their own single
// transaction (never as a trailing, separately-committed pass — a remap
// that revoked the old owner's access but crashed before granting the new
// one, or vice versa, would leave a real gap either way).
func recomputeForOwnerTx(ctx context.Context, tx pgx.Tx, incumbentUserID string) error {
	if incumbentUserID == "" {
		return fmt.Errorf("overlay: recompute requires a non-empty incumbent user id")
	}
	// No row-level locking here: every transaction that mutates this
	// workspace's visibility projection (this recompute's callers, plus
	// Ingest's owner-reassignment projection) first acquires the single
	// per-workspace visibility advisory lock (lockWorkspaceVisibility), so
	// recomputes and reassignments already run serially — a plain read of
	// the owner's current records is consistent within that serialization.
	// A per-owner FOR UPDATE was tried and rejected: it locked only rows
	// ALREADY owned by this owner, missing a record transitioning INTO the
	// owner concurrently, and two recomputes locking owner rows in opposite
	// order could deadlock. One coarse lock avoids both.
	rows, err := tx.Query(ctx,
		`SELECT object_class, external_id FROM overlay_mirror WHERE owner_external_id = $1`,
		incumbentUserID)
	if err != nil {
		return fmt.Errorf("overlay: listing records owned by %s: %w", incumbentUserID, err)
	}
	type ref struct{ objectClass, externalID string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.objectClass, &r.externalID); err != nil {
			rows.Close()
			return fmt.Errorf("overlay: scanning a record owned by %s: %w", incumbentUserID, err)
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	for _, r := range refs {
		if err := ProjectOwnerVisibility(ctx, tx, r.objectClass, r.externalID, incumbentUserID); err != nil {
			return err
		}
	}
	return nil
}

// RecomputeForOwner is recomputeForOwnerTx wrapped in its own
// WithWorkspaceTx — the externally-callable form kept for callers (tests,
// any future ad-hoc admin repair) that have no transaction of their own to
// join.
func (s *MirrorStore) RecomputeForOwner(ctx context.Context, incumbentUserID string) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := lockWorkspaceVisibility(ctx, tx); err != nil {
			return err
		}
		return recomputeForOwnerTx(ctx, tx, incumbentUserID)
	})
}

// lockWorkspaceVisibility serializes every transaction that mutates this
// workspace's mirror_visibility projection — the mapping upsert+recompute
// (UpsertUserMap), the owner-reassignment projection (Ingest), the periodic
// revalidation (RevalidateEmailMappings), and the ambiguity revoke
// (revokeEmailMappingsForOwners). ONE per-workspace advisory lock,
// transaction-scoped (auto-released at commit/rollback), is the whole
// serialization: because every visibility mutator acquires the SAME key
// FIRST, no two interleave their read-decide-clear-then-grant sequences —
// which the fine-grained per-owner/per-user locking could not guarantee (a
// record transitioning INTO an owner, a revoke racing a re-seed, or two
// recomputes acquiring owner rows in opposite order and deadlocking all
// serialize on this single lock instead). It is re-entrant within a
// transaction, so a caller that already holds it may acquire it again
// harmlessly. Overlay visibility mutations are low-frequency (a single
// leader-elected poller plus occasional manual remaps), so serializing them
// per workspace costs effectively nothing.
func lockWorkspaceVisibility(ctx context.Context, tx pgx.Tx) error {
	// current_setting WITHOUT missing_ok: an unset app.workspace_id must
	// RAISE, never resolve to NULL. hashtext and pg_advisory_xact_lock are
	// STRICT, so a NULL workspace id would turn this into a no-op SELECT
	// that acquires NO lock — silently bypassing the serialization every
	// caller relies on. Failing closed on an unset GUC (this is only ever
	// called inside database.WithWorkspaceTx, which sets it) matches how the
	// RLS policies fail closed on the same condition.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext('margince:overlay-visibility:' || current_setting('app.workspace_id'))::bigint)`); err != nil {
		return fmt.Errorf("overlay: acquiring the workspace visibility lock: %w", err)
	}
	return nil
}

// normalizeEmail applies the case/whitespace normalization design.md §4.6
// rule (1) pins for the email-match: "match on case/whitespace-normalized
// email."
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

const upsertUserMapSQL = `
INSERT INTO mirror_user_map (workspace_id, app_user_id, incumbent, incumbent_user_id, match_source)
VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, $2, $3, $4)
ON CONFLICT (workspace_id, app_user_id, incumbent) DO UPDATE
   SET incumbent_user_id = EXCLUDED.incumbent_user_id, match_source = EXCLUDED.match_source`

// selectPriorIncumbentUserIDSQL reads the (workspace, appUser, incumbent)
// mapping's CURRENT incumbent_user_id, before UpsertUserMap's own upsert
// overwrites it — the only way to learn who the OLD owner was, since
// upsertUserMapSQL's ON CONFLICT clobbers incumbent_user_id in place
// rather than versioning it.
const selectPriorIncumbentUserIDSQL = `
SELECT incumbent_user_id FROM mirror_user_map
WHERE app_user_id = $1 AND incumbent = $2`

// UpsertUserMap writes one mirror_user_map row and, in the SAME
// transaction, recomputes visibility for both the newly-mapped incumbent
// user's records AND — when this call REMAPS appUser away from a
// different, previously-mapped incumbent user — the OLD incumbent user's
// records too. Without the old-owner recompute a remap only ever grants:
// the new owner's records gain appUser's can_see=true row, but the old
// owner's records keep whatever stale can_see=true row the prior mapping
// left behind, so a remapped user silently retains access to records they
// should no longer see. Doing both recomputes inside ONE
// database.WithWorkspaceTx alongside the upsert makes the whole
// remap — write + revoke-old + grant-new — atomic: a crash or error
// partway through can never leave the mapping row and the visibility
// projections disagreeing about who currently has access.
//
// It also enforces design.md §4.6's pinned email rule: for
// match_source="email" the mapping is verified against the incumbent's
// own current owner email via OwnerEmailResolver, normalized on both
// sides (rule 1); a mismatch, or an owner email this build cannot
// resolve, is treated as "zero match" — NO row is written, fail-closed
// (rule 3). match_source="manual" is the admin escape hatch (rule 4): it
// bypasses the email check entirely, because a human already vouched for
// the mapping.
func (s *MirrorStore) UpsertUserMap(ctx context.Context, appUser ids.UserID, incumbent, incumbentUserID, source string) error {
	if incumbent == "" || incumbentUserID == "" {
		return fmt.Errorf("overlay: no mirror_user_map row for a zero-match incumbent user (fail-closed, design.md §4.6 rule 3)")
	}
	switch source {
	case "email", "manual":
	default:
		return fmt.Errorf("overlay: unknown mirror_user_map match_source %q", source)
	}

	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Resolve the email match BEFORE taking the visibility lock: the
		// match may call the incumbent (OwnerEmailResolver → HubSpot), and a
		// slow lookup must never be held under the workspace-wide lock where
		// it would block every sibling remap.
		if source == "email" {
			matched, err := s.emailMatches(ctx, tx, appUser, incumbentUserID)
			if err != nil {
				return err
			}
			if !matched {
				return fmt.Errorf("overlay: %s's email does not match incumbent user %s (fail-closed, design.md §4.6 rule 3, no row written)",
					appUser, incumbentUserID)
			}
		}

		// The disconnect-race fence, taken only for the sweep's store
		// (WithFence) — after the email resolution above so a slow incumbent
		// lookup is never held under a lock, before the write below so a
		// mapping is never resurrected into a disconnected workspace.
		if s.fenced {
			if err := assertActiveConnection(ctx, tx); err != nil {
				return err
			}
		}

		// Serialize the read-decide-upsert-recompute sequence against every
		// other visibility mutation in this workspace (a sibling remap, an
		// Ingest owner reassignment, the ambiguity revoke). Acquiring the
		// per-workspace visibility lock here — before the prior-mapping read
		// — is what makes the whole sequence atomic: without it two
		// concurrent remaps could each read the prior mapping, then
		// interleave their clear-then-grant recomputes and leave the user
		// granted on both the old and new owners' records.
		if err := lockWorkspaceVisibility(ctx, tx); err != nil {
			return err
		}

		var priorIncumbentUserID string
		err := tx.QueryRow(ctx, selectPriorIncumbentUserIDSQL, appUser, incumbent).Scan(&priorIncumbentUserID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("overlay: reading %s's prior mapping for %s: %w", appUser, incumbent, err)
		}
		// A pgx.ErrNoRows prior mapping just means appUser had never
		// mapped to this incumbent before — nothing to revoke.
		remapped := err == nil && priorIncumbentUserID != incumbentUserID

		if _, err := tx.Exec(ctx, upsertUserMapSQL, appUser, incumbent, incumbentUserID, source); err != nil {
			return fmt.Errorf("overlay: writing mirror_user_map for %s: %w", appUser, err)
		}

		if err := recomputeForOwnerTx(ctx, tx, incumbentUserID); err != nil {
			return err
		}
		if remapped {
			if err := recomputeForOwnerTx(ctx, tx, priorIncumbentUserID); err != nil {
				return err
			}
		}
		return nil
	})
}

// emailMatches compares appUser's stored email against incumbentUserID's
// current email through OwnerEmailResolver. A missing app user answers
// false (nothing to match against). Any OTHER resolution failure —
// notably OwnerEmailResolver being unable to name the incumbent's owner —
// is returned as an error rather than silently coerced to "no match": the
// caller (UpsertUserMap) still ends up writing no row either way (rule
// 3's fail-closed outcome), but it surfaces WHY, instead of masking a
// resolver fault as an ordinary email mismatch.
func (s *MirrorStore) emailMatches(ctx context.Context, tx pgx.Tx, appUser ids.UserID, incumbentUserID string) (bool, error) {
	var appEmail string
	err := tx.QueryRow(ctx, `SELECT email FROM app_user WHERE id = $1`, appUser).Scan(&appEmail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("overlay: resolving the user's email: %w", err)
	}
	ownerEmail, err := s.emails.OwnerEmail(ctx, incumbentUserID)
	if err != nil {
		return false, fmt.Errorf("overlay: resolving incumbent owner email for %s: %w", incumbentUserID, err)
	}
	normalized := normalizeEmail(appEmail)
	return normalized != "" && normalized == normalizeEmail(ownerEmail), nil
}

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
// workspace connection (compose/jobs.go's reconcileConnection), with
// emails bound to that sweep's own live incumbent adapter so the email
// this checks against is the incumbent's current value, not a stale one
// resolved at MirrorStore construction time.
func (s *MirrorStore) RevalidateEmailMappings(ctx context.Context, emails OwnerEmailResolver) error {
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
	if err != nil {
		return err
	}

	for _, owner := range owners {
		if err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
			if err := lockWorkspaceVisibility(ctx, tx); err != nil {
				return err
			}
			return s.revalidateEmailMapping(ctx, tx, emails, owner)
		}); err != nil {
			return fmt.Errorf("overlay: revalidating the email-sourced mapping for owner %s: %w", owner, err)
		}
	}
	return nil
}
