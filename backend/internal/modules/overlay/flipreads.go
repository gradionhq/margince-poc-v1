// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The flip's estate reads (B-E18.27): the migration engine imports the
// WHOLE frozen mirror, so these reads are deliberately visibility-BLIND —
// the flip migrates the workspace's estate, not one caller's view of it
// (the same whole-estate posture privacy's SAR assembly takes). The gate
// compensating for that blindness is the flip's own: every method
// requires the overlay_connection UPDATE grant, the admin/ops-only
// posture the flip execute itself carries — a rep can neither run the
// flip nor read the estate through its seam.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// FlipCounts is the estate's per-class row count — the parity preview's
// denominator (AC-mode-flip-7).
func (s *MirrorStore) FlipCounts(ctx context.Context) (map[string]int, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionUpdate); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT object_class, count(*) FROM overlay_mirror GROUP BY object_class`)
		if err != nil {
			return fmt.Errorf("overlay: counting the mirror estate for the flip: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var class string
			var n int
			if err := rows.Scan(&class, &n); err != nil {
				return fmt.Errorf("overlay: scanning a mirror estate count: %w", err)
			}
			counts[class] = n
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

// FlipRows pages one object class of the frozen estate in a stable
// order (external_id) — the migration engine's checkpoint contract
// depends on that determinism.
func (s *MirrorStore) FlipRows(ctx context.Context, objectClass string, offset, limit int) ([]Row, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionUpdate); err != nil {
		return nil, err
	}
	var out []Row
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT object_class, external_id, fields, updated_at_baseline,
			       coalesce(owner_external_id, ''), sync_state, last_synced_at
			FROM overlay_mirror
			WHERE object_class = $1
			ORDER BY external_id
			OFFSET $2 LIMIT $3`, objectClass, offset, limit)
		if err != nil {
			return fmt.Errorf("overlay: reading %s estate rows for the flip: %w", objectClass, err)
		}
		defer rows.Close()
		for rows.Next() {
			var r Row
			if err := rows.Scan(&r.ObjectClass, &r.ExternalID, &r.Fields, &r.UpdatedAtBaseline,
				&r.OwnerExternalID, &r.SyncState, &r.LastSyncedAt); err != nil {
				return fmt.Errorf("overlay: scanning a %s estate row for the flip: %w", objectClass, err)
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FlipAssociations reads every mirrored association edge — the flip's
// relationship-preservation input (AC-OV-10).
func (s *MirrorStore) FlipAssociations(ctx context.Context) ([]Assoc, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionUpdate); err != nil {
		return nil, err
	}
	var out []Assoc
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT from_type, from_id, to_type, to_id, type_id, category, coalesce(label, ''), direction
			FROM overlay_association
			ORDER BY from_type, from_id, to_type, to_id, type_id`)
		if err != nil {
			return fmt.Errorf("overlay: reading association edges for the flip: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var a Assoc
			if err := rows.Scan(&a.FromType, &a.FromID, &a.ToType, &a.ToID, &a.TypeID, &a.Category, &a.Label, &a.Direction); err != nil {
				return fmt.Errorf("overlay: scanning an association edge for the flip: %w", err)
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveMirrorOwner maps an incumbent owner id to the mapped app_user,
// via mirror_user_map — the flip writer's owner resolution. found=false
// means unmapped (the row imports ownerless, disclosed), never an error.
func (s *MirrorStore) ResolveMirrorOwner(ctx context.Context, incumbentUserID string) (ids.UUID, bool, error) {
	if err := auth.Require(ctx, overlayConnectionObject, principal.ActionUpdate); err != nil {
		return ids.UUID{}, false, err
	}
	if incumbentUserID == "" {
		return ids.UUID{}, false, nil
	}
	var id ids.UUID
	found := false
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`SELECT app_user_id FROM mirror_user_map WHERE incumbent_user_id = $1`, incumbentUserID,
		).Scan(&id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("overlay: resolving a mirror owner for the flip: %w", err)
		}
		found = true
		return nil
	})
	if err != nil {
		return ids.UUID{}, false, err
	}
	return id, found, nil
}
