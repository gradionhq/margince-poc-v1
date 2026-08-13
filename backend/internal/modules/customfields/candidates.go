// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

// The read the automation module's date_field_approaching clock scan
// needs (automation/seams.go's DateFieldScan): which records carry a
// cf_* DATE column whose value falls in a caller-given window, real or
// yearly-recurring. Sourced from this module's OWN
// catalog (ActiveColumns, catalogreader.go) plus the record table the
// catalog names — same posture as activities/lasttouch.go reading tables
// it does not own the writes to: a module reaches records only through
// seams (ADR-0054 §9), and this file is the seam's implementation,
// adapted onto automation.DateFieldScan in compose/timescan.go.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// ErrUnknownDateColumn refuses a (object, column) pair that is not, right
// now, an ACTIVE date-typed custom field on that object — an unknown
// column, a retired one, or one of a different type (text/number/…).
// DateFieldCandidates checks this BEFORE the column name ever reaches
// SQL: the caller is workspace-controlled input (an automation
// instance's own params), never a hard-coded literal, so validating
// against the workspace's own catalog is the whole safety argument for
// building a query around it at all.
var ErrUnknownDateColumn = errors.New("customfields: not an active date-typed custom field on this object")

// DateFieldCandidate is one record whose watched date field falls inside
// the caller's scan window.
type DateFieldCandidate struct {
	EntityID ids.UUID
	// StoredValue is the column's value exactly as the database holds
	// it — for a one-time field this IS the anchor a caller should act
	// on.
	StoredValue time.Time
	// OccurrenceDate is the date this scan pass is actually measuring
	// against: for a one-time field it equals StoredValue; for a
	// recurring field it is StoredValue's month/day projected onto
	// whichever of the scan window's two years that occurrence falls
	// in (see projectOccurrence) — never the year the value happens to
	// be stored with, which for a recurring field (a birthday, an
	// anniversary) carries no meaning of its own.
	OccurrenceDate time.Time
}

// minDateFieldCandidatesLimit is a FLOOR, not a cap: it raises an
// unreasonable caller-given limit up to at least 1, the same way
// clockScanBatchLimit bounds automation's own batch on the other end — a
// negative or zero limit here would either error out inside Postgres or
// (worse, LIMIT 0 is valid SQL) silently return nothing. This never
// lowers a caller's limit; DateFieldCandidates has no ceiling of its own.
const minDateFieldCandidatesLimit = 1

// DateFieldCandidates answers which records of object carry column
// (a real, active date-typed custom field, checked against the
// workspace's own catalog before column reaches SQL) with a value in
// [from, to] — literally when recurring is false, by MONTH/DAY when
// recurring is true (a window that crosses Dec 31 → Jan 1 matches via an
// OR of two MMDD ranges rather than a single BETWEEN, since MMDD is not
// a linearly ordered domain across that boundary).
//
// limit bounds the query exactly like clockScanBatchLimit bounds
// activities.LastTouchBefore (automation/timescan.go) — one scan pass
// draws a bounded batch, never the whole table.
func (s *Service) DateFieldCandidates(ctx context.Context, object, column string, from, to time.Time, recurring bool, limit int) ([]DateFieldCandidate, error) {
	if err := auth.Require(ctx, object, principal.ActionRead); err != nil {
		return nil, err
	}
	cols, err := s.ActiveColumns(ctx, object)
	if err != nil {
		return nil, fmt.Errorf("customfields: loading %s's active columns: %w", object, err)
	}
	if err := validateDateColumn(cols, column); err != nil {
		return nil, err
	}
	if limit < minDateFieldCandidatesLimit {
		limit = minDateFieldCandidatesLimit
	}

	quotedTable := quoteIdentifier(object)
	quotedCol := quoteIdentifier(column)

	var rows []dateFieldRow
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var qErr error
		if recurring {
			rows, qErr = queryRecurringCandidates(ctx, tx, quotedTable, quotedCol, from, to, limit)
		} else {
			rows, qErr = queryLiteralCandidates(ctx, tx, quotedTable, quotedCol, from, to, limit)
		}
		return qErr
	})
	if err != nil {
		return nil, err
	}

	out := make([]DateFieldCandidate, len(rows))
	for i, r := range rows {
		occurrence := r.value
		if recurring {
			occurrence = projectOccurrence(r.value.Month(), r.value.Day(), from, to)
		}
		out[i] = DateFieldCandidate{EntityID: r.id, StoredValue: r.value, OccurrenceDate: occurrence}
	}
	return out, nil
}

// validateDateColumn refuses any column not present in cols as an active
// date-typed field — the check that must run before column is ever
// formatted into a SQL string (DateFieldCandidates never trusts a
// caller-given identifier further than this).
func validateDateColumn(cols []fieldcatalog.Column, column string) error {
	for _, c := range cols {
		if c.Name == column && c.Type == fieldcatalog.TypeDate {
			return nil
		}
	}
	return fmt.Errorf("%q: %w", column, ErrUnknownDateColumn)
}

// dateFieldRow is one scanned (id, column value) pair, before the
// recurring case's occurrence projection runs.
type dateFieldRow struct {
	id    ids.UUID
	value time.Time
}

// queryLiteralCandidates is the one-time-field shape: a plain BETWEEN
// over the column's real stored value. quotedTable/quotedCol are already
// validated identifiers (DateFieldCandidates), safe to format directly.
func queryLiteralCandidates(ctx context.Context, tx pgx.Tx, quotedTable, quotedCol string, from, to time.Time, limit int) ([]dateFieldRow, error) {
	query := fmt.Sprintf(
		`SELECT id, %[1]s FROM %[2]s WHERE %[1]s BETWEEN $1 AND $2 ORDER BY %[1]s, id LIMIT $3`,
		quotedCol, quotedTable)
	return scanDateFieldRows(ctx, tx, query, limit, from, to)
}

// queryRecurringCandidates is the yearly-recurrence shape: compares the
// column's MONTH/DAY (to_char(...,'MMDD')) against the window's own
// MMDD bounds. A window that does not cross the year boundary
// (fromMMDD <= toMMDD) is a single BETWEEN; a window that crosses Dec 31
// → Jan 1 (fromMMDD > toMMDD, e.g. Dec 20 → Jan 15) cannot be expressed
// as one BETWEEN over a non-cyclic string domain, so it becomes an OR of
// the two ranges either side of the wrap.
func queryRecurringCandidates(ctx context.Context, tx pgx.Tx, quotedTable, quotedCol string, from, to time.Time, limit int) ([]dateFieldRow, error) {
	fromMMDD := from.Format("0102")
	toMMDD := to.Format("0102")
	var predicate string
	if fromMMDD <= toMMDD {
		predicate = fmt.Sprintf(`to_char(%[1]s,'MMDD') BETWEEN $1 AND $2`, quotedCol)
	} else {
		predicate = fmt.Sprintf(`(to_char(%[1]s,'MMDD') >= $1 OR to_char(%[1]s,'MMDD') <= $2)`, quotedCol)
	}
	query := fmt.Sprintf(`SELECT id, %[1]s FROM %[2]s WHERE %[3]s ORDER BY %[1]s, id LIMIT $3`,
		quotedCol, quotedTable, predicate)
	return scanDateFieldRows(ctx, tx, query, limit, fromMMDD, toMMDD)
}

// scanDateFieldRows runs query (already built with validated identifiers
// by its caller) and scans the (id, date) result shape both candidate
// queries share. args are the query's own $1/$2 binds in order — a
// time.Time pair for the literal shape, a MMDD string pair for the
// recurring shape — with limit always bound last as $3, since every
// candidate query shares that same LIMIT position regardless of what its
// own predicate binds.
func scanDateFieldRows(ctx context.Context, tx pgx.Tx, query string, limit int, args ...any) ([]dateFieldRow, error) {
	rows, err := tx.Query(ctx, query, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dateFieldRow
	for rows.Next() {
		var r dateFieldRow
		if err := rows.Scan(&r.id, &r.value); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// projectOccurrence answers which of the scan window's two candidate
// years (from's or to's) a recurring field's month/day belongs to this
// pass, given only the value's own month and day — the stored YEAR
// carries no meaning for a recurring field (custom-fields.md: a
// birthday's stored value never changes year to year).
//
// A window that does not cross the year boundary has exactly one
// candidate year (to's — the window's own later, "current" boundary);
// a window that DOES cross Dec 31 → Jan 1 splits in two: an MMDD on or
// after fromMMDD is this pass's DECEMBER side and belongs to from's
// year, everything else is the JANUARY side and belongs to to's year.
//
// time.Date normalizes an out-of-range day for its month (most visibly
// Feb 29 in a non-leap projected year, which rolls to Mar 1) — accepted
// as-is, not special-cased: nobody has asked this trigger to treat a
// leap-day field specially, and inventing behaviour nobody asked for is
// its own kind of bug.
func projectOccurrence(month time.Month, day int, from, to time.Time) time.Time {
	fromMMDD := from.Format("0102")
	toMMDD := to.Format("0102")
	if fromMMDD <= toMMDD {
		return time.Date(to.Year(), month, day, 0, 0, 0, 0, to.Location())
	}
	candMMDD := fmt.Sprintf("%02d%02d", int(month), day)
	if candMMDD >= fromMMDD {
		return time.Date(from.Year(), month, day, 0, 0, 0, 0, from.Location())
	}
	return time.Date(to.Year(), month, day, 0, 0, 0, 0, to.Location())
}
