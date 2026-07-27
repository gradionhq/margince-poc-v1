// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// auditEntityUserMap is the audit_log.entity_type every mirror_user_map
// mutation is recorded under. mirror_user_map has no id column, so the audit
// keys on the mapping's subject — the app_user it governs.
const auditEntityUserMap = "mirror_user_map"

// auditEntityAutoMapBlock is the audit_log.entity_type an admin's auto-map
// block is recorded under. Its own type, not mirror_user_map's: the block is a
// standing decision about FUTURE automatic mapping, not the removal of a
// mapping row, and folding it into the mapping's trail would make an unmap of
// an already-unmapped user read as the deletion of a row that never existed.
// Keyed on the same subject — the app_user the decision governs — because
// mirror_user_automap_block has no id column either.
const auditEntityAutoMapBlock = "mirror_user_automap_block"

// userMapImage is the before/after field image an audited mapping change
// records. It carries the row's OWN fields only: operation metadata folded in
// here would make downstream field-history projections read it as field
// changes that never happened on the record.
type userMapImage struct {
	IncumbentUserID string `json:"incumbent_user_id"`
	MatchSource     string `json:"match_source"`
}

// autoMapBlockImage is the after-image an auto-map block records. Same
// row-fields-only rule as userMapImage, and the fields it omits are the ones
// the audit row itself already carries: blocked_by IS the actor, blocked_at IS
// the audit timestamp.
type autoMapBlockImage struct {
	Incumbent string `json:"incumbent"`
}

// revokedMapping is one mirror_user_map row an automated revoke deleted: the
// app_user the audit keys on, plus the image that vanished with it. Both
// revoke paths (a stale email in emailrevalidate.go, an email that turned
// ambiguous in usermapseed.go) delete a SET of rows, so each needs the
// per-row identity a bare RowsAffected count cannot give — an admin asking
// why a user lost access needs to know which mapping went, not how many did.
type revokedMapping struct {
	appUser ids.UserID
	image   userMapImage
}

// collectRevokedMapping reads one `DELETE … RETURNING app_user_id,
// incumbent_user_id, match_source` row, for pgx.CollectRows.
func collectRevokedMapping(row pgx.CollectableRow) (revokedMapping, error) {
	var r revokedMapping
	if err := row.Scan(&r.appUser, &r.image.IncumbentUserID, &r.image.MatchSource); err != nil {
		return revokedMapping{}, fmt.Errorf("overlay: scanning a revoked mirror_user_map row: %w", err)
	}
	return r, nil
}

// auditUserMapChange records a mapping write at the single insert site, so
// the sweep's seeding and an admin's manual pin are both covered by
// construction rather than by remembering to call this from each caller.
//
// Only a REAL change is audited: the sweep re-seeds every tick, and auditing
// an unchanged row would write one line per owner per tick until the audit
// log stops being readable evidence. The comparison covers match_source as
// well as the owner — an email->manual flip on the same owner makes the row
// immune to revalidation and to the sweep, which is a governance change even
// though the owner did not move.
func auditUserMapChange(ctx context.Context, tx pgx.Tx, appUser ids.UserID, prior userMapImage, existed bool, next userMapImage) error {
	if existed && prior == next {
		return nil
	}
	action := "update"
	var before any
	if !existed {
		action = "create"
	} else {
		before = prior
	}
	if _, err := storekit.Audit(ctx, tx, action, auditEntityUserMap, appUser.UUID, before, next); err != nil {
		return fmt.Errorf("overlay: auditing %s's mapping change: %w", appUser, err)
	}
	return nil
}

// UserMapEntry is one row of the admin mapping table: a workspace user, the
// incumbent user they currently map to (empty when unmapped), how that
// mapping was established, and whether an admin has blocked automatic
// mapping for them.
type UserMapEntry struct {
	AppUserID       ids.UserID
	Email           string
	Name            string
	IncumbentUserID string
	MatchSource     string
	Blocked         bool
}

// listUserMapSQL pages the workspace's users with their mapping state.
// LEFT JOINs, not inner ones: an UNMAPPED user is the whole point of the
// surface — they are the ones an admin has to act on — so they must appear
// with an empty owner rather than be filtered out. Keyset-ordered by id so
// the cursor is stable across pages.
//
// Agent and archived users are excluded: a passport identity has no incumbent
// counterpart to map, and offering an archived user a mapping affordance
// invites an admin to grant visibility to a seat that no longer logs in.
const listUserMapSQL = `
SELECT u.id, u.email, u.display_name,
       coalesce(m.incumbent_user_id, ''), coalesce(m.match_source, ''),
       b.app_user_id IS NOT NULL
FROM app_user u
LEFT JOIN mirror_user_map m
       ON m.app_user_id = u.id AND m.incumbent = $1
LEFT JOIN mirror_user_automap_block b
       ON b.app_user_id = u.id AND b.incumbent = $1
WHERE u.id > $2
  AND NOT u.is_agent
  AND u.archived_at IS NULL
ORDER BY u.id
LIMIT $3`

// ListUserMap pages this workspace's users with their current mapping state,
// in app_user id order — the same opaque keyset cursor scheme MirrorStore.List
// uses, carrying a user id instead of an external id. It is a plain
// workspace-scoped read (RLS confines it to the tenant); the admin-only gate
// lives at the service entry point, with every other user-map operation.
func (s *MirrorStore) ListUserMap(ctx context.Context, incumbent, cursor string, limit int) ([]UserMapEntry, string, error) {
	after, err := decodeMirrorCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	switch {
	case limit <= 0:
		limit = defaultListLimit
	case limit > maxListLimit:
		limit = maxListLimit
	}
	var afterID ids.UserID
	if after != "" {
		afterID, err = ids.ParseAs[ids.UserKind](after)
		if err != nil {
			// A token that decodes but does not carry a user id is the same
			// client fault as one that does not decode at all — this cursor's
			// payload IS a user id, so anything else was never minted here.
			return nil, "", &storekit.MalformedCursorError{}
		}
	}

	var entries []UserMapEntry
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listUserMapSQL, incumbent, afterID, limit)
		if err != nil {
			return fmt.Errorf("overlay: listing the user map for %s: %w", incumbent, err)
		}
		defer rows.Close()
		for rows.Next() {
			var e UserMapEntry
			if err := rows.Scan(&e.AppUserID, &e.Email, &e.Name,
				&e.IncumbentUserID, &e.MatchSource, &e.Blocked); err != nil {
				return fmt.Errorf("overlay: scanning a user-map row: %w", err)
			}
			entries = append(entries, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(entries) == limit {
		next = encodeMirrorCursor(entries[len(entries)-1].AppUserID.String())
	}
	return entries, next, nil
}

// selectUserMapTargetSQL reads what a user-map operation needs to know about
// its target: that it exists at all, and whether it is a seat an admin may
// GRANT a mapping to.
//
// The eligibility half is ONE invariant with two siblings that must move with
// it: listUserMapSQL (what the admin surface offers) and usersMatchingEmail
// (usermapseed.go — what the automated sweep will seed). All three exclude an
// agent seat and an archived seat; a change here belongs in all three.
const selectUserMapTargetSQL = `
SELECT NOT u.is_agent AND u.archived_at IS NULL
FROM app_user u
WHERE u.id = $1`

// resolveUserMapTarget resolves appUser inside tx, reporting whether the seat
// is grantable — a live human. An agent seat is a passport identity with no
// incumbent counterpart, and an archived seat no longer logs in.
//
// A user this workspace does not have (including another tenant's, which RLS
// makes indistinguishable from a nonexistent one) answers
// apperrors.ErrNotFound: a row-scope miss is existence-hiding, never a 403 and
// never the raw foreign-key violation the write would otherwise raise — that
// reaches the client as an opaque 500, indistinguishable from an outage. The
// composite FK stays underneath as the database-level backstop.
func resolveUserMapTarget(ctx context.Context, tx pgx.Tx, appUser ids.UserID) (grantable bool, err error) {
	err = tx.QueryRow(ctx, selectUserMapTargetSQL, appUser).Scan(&grantable)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, apperrors.ErrNotFound
	case err != nil:
		return false, fmt.Errorf("overlay: resolving the user-map target %s: %w", appUser, err)
	}
	return grantable, nil
}

// SetManualUserMap pins appUser to incumbentUserID as a human-vouched
// override (design.md §4.6 rule 4). The "manual" source skips the email
// verification, and upsertUserMapSQL's own `cleared` CTE drops any auto-map
// block in the SAME statement — so the mapping and the block clear cannot
// disagree.
//
// This verb GRANTS mirror visibility, so its target must be a seat the admin
// surface actually offers: an agent or archived user answers
// apperrors.ErrNotFound, because ListUserMap does not list them and an
// unaddressable target owes existence-hiding rather than a 403. The gate runs
// in the SAME transaction as the write (upsertUserMapTx), so the seat cannot
// change state between the decision and the row it authorizes.
func (s *MirrorStore) SetManualUserMap(ctx context.Context, appUser ids.UserID, incumbent, incumbentUserID string) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		grantable, err := resolveUserMapTarget(ctx, tx, appUser)
		if err != nil {
			return err
		}
		if !grantable {
			return apperrors.ErrNotFound
		}
		return s.upsertUserMapTx(ctx, tx, appUser, incumbent, incumbentUserID, "manual")
	})
}

const insertAutomapBlockSQL = `
INSERT INTO mirror_user_automap_block (workspace_id, app_user_id, incumbent, blocked_by)
VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, $2, $3)
ON CONFLICT (workspace_id, app_user_id, incumbent) DO NOTHING`

const deleteUserMapSQL = `
DELETE FROM mirror_user_map
WHERE app_user_id = $1 AND incumbent = $2
RETURNING incumbent_user_id, match_source`

// BlockAutoMap removes appUser's mapping and records that an admin
// deliberately unmapped them, so the reconcile sweep's email matching cannot
// map them again. All three effects commit together: without the
// recomputeForOwnerTx the mirror_visibility grants the old mapping produced
// would survive the delete and keep serving records to a user who is no
// longer mapped, and without the block row the next sweep would simply
// re-create the mapping.
//
// Idempotent: unmapping an already-unmapped user still records the decision,
// so a retry is not an error.
func (s *MirrorStore) BlockAutoMap(ctx context.Context, appUser ids.UserID, incumbent string) error {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return errors.New("overlay: no principal bound to context")
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Fence before the visibility lock — the order every other visibility
		// mutator takes (UpsertUserMap, ingestTx, RecomputeForOwner), so no two
		// fenced writers can deadlock by acquiring the two in opposite orders
		// and a doomed transaction never holds the workspace-wide lock while
		// failing.
		if err := s.assertFence(ctx, tx); err != nil {
			return err
		}
		// Existence only — deliberately NOT the grantable check
		// SetManualUserMap makes. This verb REMOVES access, and refusing to
		// unmap an agent or archived seat is the fail-open direction: a user
		// archived while mapped is precisely who an admin still has to be able
		// to unmap. Resolved before the lock so a request naming a user this
		// workspace does not have never holds the workspace-wide lock.
		if _, err := resolveUserMapTarget(ctx, tx, appUser); err != nil {
			return err
		}
		if err := lockWorkspaceVisibility(ctx, tx); err != nil {
			return err
		}

		var prior userMapImage
		err := tx.QueryRow(ctx, deleteUserMapSQL, appUser, incumbent).
			Scan(&prior.IncumbentUserID, &prior.MatchSource)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// Already unmapped; the block below still records the decision.
		case err != nil:
			return fmt.Errorf("overlay: removing %s's mapping for %s: %w", appUser, incumbent, err)
		default:
			if _, err := storekit.Audit(ctx, tx, "archive", auditEntityUserMap, appUser.UUID, prior, nil); err != nil {
				return fmt.Errorf("overlay: auditing %s's admin unmap: %w", appUser, err)
			}
			if err := recomputeForOwnerTx(ctx, tx, prior.IncumbentUserID); err != nil {
				return err
			}
		}

		tag, err := tx.Exec(ctx, insertAutomapBlockSQL, appUser, incumbent, actor.UserID)
		if err != nil {
			return fmt.Errorf("overlay: recording the auto-map block for %s: %w", appUser, err)
		}
		// The block is its own governance fact and needs its own record: it
		// permanently stops the sweep from mapping this user, and on the
		// already-unmapped path it is the ONLY thing that changed — the archive
		// above never ran, so without this the decision leaves no trace, and
		// "who decided this user sees nothing?" has no answer once a disconnect
		// purges the block row's own blocked_by.
		//
		// ON CONFLICT DO NOTHING makes a repeat call affect no rows, which is
		// exactly the signal that separates the decision from a retry of it:
		// auditing the retry would claim a decision nobody took twice.
		if tag.RowsAffected() == 0 {
			return nil
		}
		if _, err := storekit.Audit(ctx, tx, "create", auditEntityAutoMapBlock, appUser.UUID,
			nil, autoMapBlockImage{Incumbent: incumbent}); err != nil {
			return fmt.Errorf("overlay: auditing %s's auto-map block: %w", appUser, err)
		}
		return nil
	})
}
