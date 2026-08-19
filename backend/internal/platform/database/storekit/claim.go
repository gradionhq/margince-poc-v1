// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Claim is what ClaimOwnership answers: who owned the row before (nil for an
// unowned one), the row's version after, and whether anything was written —
// a claim of a row already one's own is a no-op that writes no audit noise.
type Claim struct {
	Before  *ids.UUID
	Version int64
	Changed bool
}

// ClaimOwnership makes `me` the owner of one row of a record table: the one
// spelling of "put my name on this" for every record type a seat can own.
// The row is locked, gated by auth.EnsureClaimable (visible, and unowned or
// already the caller's to change), compared against ifVersion when given,
// and updated against the owner just read — a claim that raced another one
// finds the winner's name and answers ErrConflict. The caller writes the
// record's own updated event beside the audit row this returns with.
func ClaimOwnership(ctx context.Context, tx pgx.Tx, table string, id, me ids.UUID, ifVersion *int64) (Claim, ids.UUID, error) {
	if _, err := LockRow(ctx, tx, table, id, LiveOnly); err != nil {
		return Claim{}, ids.Nil, err
	}
	if err := auth.EnsureClaimable(ctx, tx, table, id); err != nil {
		return Claim{}, ids.Nil, err
	}
	var before *ids.UUID
	var version int64
	if err := tx.QueryRow(ctx, SQLf(`SELECT owner_id, version FROM %s WHERE id = $1`, table), id).
		Scan(&before, &version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Claim{}, ids.Nil, apperrors.ErrNotFound
		}
		return Claim{}, ids.Nil, err
	}
	if ifVersion != nil && version != *ifVersion {
		return Claim{}, ids.Nil, apperrors.ErrVersionSkew
	}
	if before != nil && *before == me {
		return Claim{Before: before, Version: version}, ids.Nil, nil
	}
	tag, err := tx.Exec(ctx, SQLf(`UPDATE %s SET owner_id = $2 WHERE id = $1 AND owner_id IS NOT DISTINCT FROM $3`, table), id, me, before)
	if err != nil {
		return Claim{}, ids.Nil, err
	}
	if tag.RowsAffected() != 1 {
		return Claim{}, ids.Nil, fmt.Errorf("%w: the record was claimed by somebody else", apperrors.ErrConflict)
	}
	auditID, err := Audit(ctx, tx, "assign", table, id, map[string]any{"owner_id": before}, map[string]any{"owner_id": me})
	if err != nil {
		return Claim{}, ids.Nil, err
	}
	if err := tx.QueryRow(ctx, SQLf(`SELECT version FROM %s WHERE id = $1`, table), id).Scan(&version); err != nil {
		return Claim{}, ids.Nil, err
	}
	return Claim{Before: before, Version: version, Changed: true}, auditID, nil
}
