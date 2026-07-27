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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// auditEntityUserMap is the audit_log.entity_type every mirror_user_map
// mutation is recorded under. mirror_user_map has no id column, so the audit
// keys on the mapping's subject — the app_user it governs.
const auditEntityUserMap = "mirror_user_map"

// userMapImage is the before/after field image an audited mapping change
// records. It carries the row's OWN fields only: operation metadata folded in
// here would make downstream field-history projections read it as field
// changes that never happened on the record.
type userMapImage struct {
	IncumbentUserID string `json:"incumbent_user_id"`
	MatchSource     string `json:"match_source"`
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
			return nil, "", fmt.Errorf("overlay: malformed user-map cursor: %w", err)
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

// SetManualUserMap pins appUser to incumbentUserID as a human-vouched
// override (design.md §4.6 rule 4). It is a thin wrapper over UpsertUserMap:
// the "manual" source already skips the email verification, and
// upsertUserMapSQL's own `cleared` CTE drops any auto-map block in the SAME
// statement — so the mapping and the block clear cannot disagree, and no
// transaction plumbing is needed here.
func (s *MirrorStore) SetManualUserMap(ctx context.Context, appUser ids.UserID, incumbent, incumbentUserID string) error {
	return s.UpsertUserMap(ctx, appUser, incumbent, incumbentUserID, "manual")
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

		if _, err := tx.Exec(ctx, insertAutomapBlockSQL, appUser, incumbent, actor.UserID); err != nil {
			return fmt.Errorf("overlay: recording the auto-map block for %s: %w", appUser, err)
		}
		return nil
	})
}
