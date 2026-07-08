// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ArchivedFilter exists for call-site legibility on row-visibility
// reads: a positional bool ("readDeal(ctx, tx, id, true)") hides which
// way the archived rows go, so every by-id read spells it with these
// constants instead.
type ArchivedFilter uint8

const (
	// LiveOnly resolves only unarchived rows — the default read posture.
	LiveOnly ArchivedFilter = iota
	// IncludeArchived resolves archived rows too: archived and merged
	// records stay fetchable by id.
	IncludeArchived
)

// Patch accumulates a partial UPDATE: only fields the client sent, plus
// the before/after diff the audit row records.
type Patch struct {
	sets   []string
	args   []any
	before map[string]any
	after  map[string]any
}

func NewPatch() *Patch {
	return &Patch{before: map[string]any{}, after: map[string]any{}}
}

// Set records one changed column. oldVal comes from the row read inside
// the same transaction, so the audit diff is exact.
//
//craft:ignore naked-any column values span every SQL type a module owns; they flow to bind parameters and the schemaless audit diff
func (p *Patch) Set(column string, oldVal, newVal any) {
	p.args = append(p.args, newVal)
	p.sets = append(p.sets, fmt.Sprintf("%s = $%d", column, len(p.args)))
	p.before[column] = oldVal
	p.after[column] = newVal
}

func (p *Patch) Empty() bool { return len(p.sets) == 0 }

// Before and After expose the audit diff the accumulated Set calls built.
func (p *Patch) Before() map[string]any { return p.before }
func (p *Patch) After() map[string]any  { return p.after }

// ApplyWithVersion runs the UPDATE under optimistic concurrency: the
// WHERE clause always carries the caller's version; zero rows affected
// on a live row means version skew (data-model §1.3a). Every mutable-row
// update carries a concurrency guard — this version check for
// client-driven edits, or a held RowLock (ApplyLocked) for internal
// flows. An unguarded update is not expressible.
func (p *Patch) ApplyWithVersion(ctx context.Context, tx pgx.Tx, table string, id ids.UUID, version int64) error {
	p.args = append(p.args, id)
	where := fmt.Sprintf("id = $%d AND archived_at IS NULL", len(p.args))
	p.args = append(p.args, version)
	where += fmt.Sprintf(" AND version = $%d", len(p.args))

	tag, err := tx.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET %s WHERE %s`, table, strings.Join(p.sets, ", "), where),
		p.args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	// Distinguish "gone" from "stale": a live row that didn't match can
	// only mean the version clause failed.
	var exists bool
	if err := tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1 AND archived_at IS NULL)`, table),
		id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return apperrors.ErrVersionSkew
	}
	return apperrors.ErrNotFound
}

// ApplyGuarded is the client-driven update seam. With an If-Match
// version it is the optimistic CAS (ApplyWithVersion); without one —
// the contract keeps If-Match optional (data-model §1.3a) — it takes
// the row lock first, so the update is still serialized against
// concurrent writers instead of racing them. Internal multi-step flows
// don't use this: they lock BEFORE their decision reads (LockRow /
// LockPair + ApplyLocked) so the read itself cannot go stale.
func (p *Patch) ApplyGuarded(ctx context.Context, tx pgx.Tx, table string, id ids.UUID, ifVersion *int64) error {
	if ifVersion != nil {
		return p.ApplyWithVersion(ctx, tx, table, id, *ifVersion)
	}
	lock, err := LockRow(ctx, tx, table, id, LiveOnly)
	if err != nil {
		return err
	}
	return p.ApplyLocked(ctx, tx, lock)
}

// RowLock witnesses that the current transaction holds FOR UPDATE on one
// live row. Its fields are unexported and only LockRow/LockPair mint it,
// so an ApplyLocked call structurally proves the row cannot race a
// concurrent writer for the rest of the transaction.
type RowLock struct {
	table string
	id    ids.UUID
}

// ID exposes the locked row's id so multi-step flows can thread the
// witness instead of a bare UUID.
func (l RowLock) ID() ids.UUID { return l.id }

// LockRow takes (or idempotently re-takes) FOR UPDATE on one row; a row
// the filter cannot resolve is apperrors.ErrNotFound. LiveOnly is the
// mutation default; IncludeArchived serves flows whose refusal
// diagnostics read archived rows (a re-promote's 409-with-pointer).
// Reads that decide a state transition belong AFTER this call — a
// pre-lock read is the TOCTOU shape this helper exists to remove.
func LockRow(ctx context.Context, tx pgx.Tx, table string, id ids.UUID, archived ArchivedFilter) (RowLock, error) {
	liveClause := " AND archived_at IS NULL"
	if archived == IncludeArchived {
		liveClause = ""
	}
	var got ids.UUID
	err := tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT id FROM %s WHERE id = $1%s FOR UPDATE`, table, liveClause),
		id).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return RowLock{}, apperrors.ErrNotFound
	}
	if err != nil {
		return RowLock{}, err
	}
	return RowLock{table: table, id: id}, nil
}

// LockPair locks two rows of one table ordered by id — the
// deadlock-safe prelude for merge-shaped flows that mutate both
// endpoints. Unlike LockRow it locks regardless of archived state: the
// caller's reads decide liveness UNDER the lock and keep their richer
// diagnostics (already-merged conflict, dead-target refusal). A row
// that does not exist at all is apperrors.ErrNotFound. The lock is
// taken before any visibility check: inside a workspace-bound
// transaction the RLS GUC already bounds what can be locked, and the
// caller's RBAC gate still decides whether the flow may proceed.
func LockPair(ctx context.Context, tx pgx.Tx, table string, a, b ids.UUID) (la, lb RowLock, err error) {
	if a == b {
		return RowLock{}, RowLock{}, errors.New("storekit: LockPair needs two distinct rows")
	}
	rows, err := tx.Query(ctx,
		fmt.Sprintf(`SELECT id FROM %s WHERE id = ANY($1) ORDER BY id FOR UPDATE`, table),
		[]ids.UUID{a, b})
	if err != nil {
		return RowLock{}, RowLock{}, err
	}
	locked := map[ids.UUID]bool{}
	var scanErr error
	for rows.Next() {
		var id ids.UUID
		if scanErr = rows.Scan(&id); scanErr != nil {
			break
		}
		locked[id] = true
	}
	rows.Close()
	if scanErr != nil {
		return RowLock{}, RowLock{}, scanErr
	}
	if err := rows.Err(); err != nil {
		return RowLock{}, RowLock{}, err
	}
	if !locked[a] || !locked[b] {
		return RowLock{}, RowLock{}, apperrors.ErrNotFound
	}
	return RowLock{table: table, id: a}, RowLock{table: table, id: b}, nil
}

// ApplyLocked runs the patch under an already-held row lock. Zero rows
// affected can only mean this same transaction archived the row after
// locking it — a programming error surfaced as ErrNotFound, never a
// silent no-op.
func (p *Patch) ApplyLocked(ctx context.Context, tx pgx.Tx, lock RowLock) error {
	if lock.table == "" {
		return errors.New("storekit: ApplyLocked requires a lock minted by LockRow or LockPair")
	}
	p.args = append(p.args, lock.id)
	tag, err := tx.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET %s WHERE id = $%d AND archived_at IS NULL`,
			lock.table, strings.Join(p.sets, ", "), len(p.args)),
		p.args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apperrors.ErrNotFound
	}
	return nil
}
