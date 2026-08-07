// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// NoArchiveColumn is for a table that has no archived_at at all — a
	// dependent row deleted with its parent rather than retired on its own,
	// such as the organization evidence sidecars. It renders the same empty
	// predicate as IncludeArchived and is a separate value on purpose: at a
	// call site, IncludeArchived reads as "this write deliberately reaches
	// archived rows", which for such a table is a claim about rows that cannot
	// exist. Stating which of the two a caller means is the whole point of this
	// type.
	NoArchiveColumn
)

// Patch accumulates a partial UPDATE: only fields the client sent, plus
// the before/after diff the audit row records.
type Patch struct {
	sets []string
	args []any
	// slotBySQLName maps an assignment's rendered left-hand side to the position
	// it already holds in sets and args, which are the same index because both
	// only ever grow together (the apply paths clone rather than append).
	//
	// An assignment list is a SET of columns: two branches of one request can each
	// want `updated_at` bumped, and appending a second assignment renders
	// `SET updated_at = $1, updated_at = $2`, which Postgres rejects (42601) —
	// turning a legal request into a 500. So a repeat lands on the first slot.
	//
	// Keyed on the RENDERED name, not the bare column, so the merge only happens
	// where both writers agree on the identifier. A core column and a catalog
	// cf_ column that somehow shared a name would keep separate slots and still
	// fail loudly, rather than one silently overwriting the other's validated
	// value — or worse, deciding whether the identifier reaches the SQL quoted.
	slotBySQLName map[string]int
	before        map[string]any
	after         map[string]any
}

func NewPatch() *Patch {
	return &Patch{slotBySQLName: map[string]int{}, before: map[string]any{}, after: map[string]any{}}
}

// Set records one changed column. oldVal comes from the row read inside
// the same transaction, so the audit diff is exact.
//
// Setting the same column twice is safe: the last value wins, in one assignment,
// and the FIRST oldVal is the one kept — so the diff spans the whole change
// rather than its last leg.
//
//craft:ignore naked-any column values span every SQL type a module owns; they flow to bind parameters and the schemaless audit diff
func (p *Patch) Set(column string, oldVal, newVal any) {
	p.recordAssignment(column, column, oldVal, newVal)
}

// setQuoted records one changed column whose SQL identifier is quoted in
// the SET fragment while the audit diff keeps the bare name. Core columns
// stay on Set (their names are fixed literals in each store's SQL);
// catalog-derived cf_ columns come through here (SetCustomFieldPatch) so
// a column name never reaches the UPDATE text unquoted, and
// audit_log.before/after keys stay wire names, not SQL identifiers.
//
//craft:ignore naked-any same column-value contract as Set
func (p *Patch) setQuoted(column string, oldVal, newVal any) {
	p.recordAssignment(column, quoteColumnIdentifier(column), oldVal, newVal)
}

// recordAssignment is the one place an assignment is recorded, so a quoted cf_
// column de-duplicates exactly as a core one does. The audit images stay keyed on
// the bare wire name; only the slot lookup uses the rendered identifier.
//
// A repeat overwrites the bound value and leaves `before` alone: the first Set
// captured what the row actually held when this transaction read it, while a
// second call's oldVal is at best the same and at worst a re-read of a value
// this transaction already changed. Keeping the first is what makes the audit
// diff describe the whole change rather than its last leg.
//
//craft:ignore naked-any same column-value contract as Set
func (p *Patch) recordAssignment(column, sqlName string, oldVal, newVal any) {
	if i, seen := p.slotBySQLName[sqlName]; seen {
		p.args[i] = newVal
		p.sets[i] = fmt.Sprintf("%s = $%d", sqlName, i+1)
		p.after[column] = newVal
		return
	}
	p.args = append(p.args, newVal)
	p.slotBySQLName[sqlName] = len(p.args) - 1
	p.sets = append(p.sets, fmt.Sprintf("%s = $%d", sqlName, len(p.args)))
	p.before[column] = oldVal
	p.after[column] = newVal
}

func (p *Patch) Empty() bool { return len(p.sets) == 0 }

// liveClause renders the archive predicate every guarded write and its
// row lock share, so a lock and the update it authorizes can never resolve
// different row sets.
func liveClause(archived ArchivedFilter) string {
	if archived == LiveOnly {
		return " AND archived_at IS NULL"
	}
	return ""
}

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
	return p.applyWithVersionIn(ctx, tx, table, id, version, LiveOnly)
}

// applyWithVersionIn is ApplyWithVersion against a chosen archive filter. It
// exists for NoArchiveColumn: LiveOnly against a table with no archived_at
// renders a predicate naming a column that is not there, and the write fails as
// a SQL error rather than as anything a caller can act on. Unexported because
// ApplyGuardedIn is the only way in — a caller choosing the filter is also a
// caller who may be handed an If-Match.
func (p *Patch) applyWithVersionIn(
	ctx context.Context, tx pgx.Tx, table string, id ids.UUID, version int64, archived ArchivedFilter,
) error {
	live := liveClause(archived)
	// The guard's binds are appended to a COPY: p.args must keep holding exactly
	// the assignments, one per entry, or the slot index above stops naming the
	// placeholder it renders. That also makes the id and version binds local to
	// this call rather than accumulating on the patch.
	args := append(slices.Clone(p.args), id, version)
	where := fmt.Sprintf("id = $%d%s AND version = $%d", len(args)-1, live, len(args))

	tag, err := tx.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET %s WHERE %s`, table, strings.Join(p.sets, ", "), where),
		args...)
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
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1%s)`, table, live),
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
	return p.ApplyGuardedIn(ctx, tx, table, id, ifVersion, LiveOnly)
}

// ApplyGuardedIn is ApplyGuarded against a chosen archive filter, for the
// tables that have no archived_at column — pass NoArchiveColumn.
func (p *Patch) ApplyGuardedIn(
	ctx context.Context, tx pgx.Tx, table string, id ids.UUID, ifVersion *int64, archived ArchivedFilter,
) error {
	if ifVersion != nil {
		return p.applyWithVersionIn(ctx, tx, table, id, *ifVersion, archived)
	}
	lock, err := LockRow(ctx, tx, table, id, archived)
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
	// archived is the filter the lock was taken under, carried so ApplyLocked
	// writes to exactly the row set LockRow resolved. Without it a lock taken on
	// a table with no archived_at would be applied through a liveness clause the
	// table cannot answer.
	archived ArchivedFilter
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
	var got ids.UUID
	err := tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT id FROM %s WHERE id = $1%s FOR UPDATE`, table, liveClause(archived)),
		id).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return RowLock{}, apperrors.ErrNotFound
	}
	if err != nil {
		return RowLock{}, err
	}
	return RowLock{table: table, id: id, archived: archived}, nil
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
	// Archived rows are locked too, deliberately. A merge has to READ a retired
	// source to resolve where it went: mergePair turns an archived row carrying
	// merged_into_id into AlreadyMergedError, which names the survivor. Filtering
	// them out here would take that redirect away and answer a bare not-found
	// instead. The LiveOnly on the returned locks governs the WRITE, which
	// targets the live survivor.
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
	// LiveOnly explicitly, not by the zero value: the pair is a merge's two
	// sides and both must be live, and a reader should not have to know which
	// constant iota lands on to be sure of it.
	return RowLock{table: table, id: a, archived: LiveOnly},
		RowLock{table: table, id: b, archived: LiveOnly}, nil
}

// ApplyLocked runs the patch under an already-held row lock. Zero rows
// affected can only mean this same transaction archived the row after
// locking it — a programming error surfaced as ErrNotFound, never a
// silent no-op.
func (p *Patch) ApplyLocked(ctx context.Context, tx pgx.Tx, lock RowLock) error {
	if lock.table == "" {
		return errors.New("storekit: ApplyLocked requires a lock minted by LockRow or LockPair")
	}
	// A copy, for the same reason ApplyWithVersion clones: the assignments and
	// their bind values stay index-aligned.
	args := append(slices.Clone(p.args), lock.id)
	tag, err := tx.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET %s WHERE id = $%d%s`,
			lock.table, strings.Join(p.sets, ", "), len(args), liveClause(lock.archived)),
		args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return apperrors.ErrNotFound
	}
	return nil
}
