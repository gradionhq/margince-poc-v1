// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The prelude every list read in this module shares: validate the sort
// against the record's vocabulary plus its live custom columns, clamp the
// page size, and build the WHERE terms that are the same for every record
// type — the caller's row scope, its custom-field equality filters, and the
// keyset cursor. What differs per record (its columns, its own filters, its
// scanner) stays with the record.

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// The column names the module's lists order by, spelled once so a
// vocabulary entry and the clause reading it cannot drift apart.
const (
	listCreatedAtColumn = "created_at"
	listUpdatedAtColumn = "updated_at"
)

// listPrelude is one list read's validated, scope-bounded starting point.
// It is passed by POINTER throughout: `arg` appends to args, and callers
// keep registering arguments (their own filters) after it is built — a
// value copy would leave those arguments on a dead struct and the query
// short of placeholders.
type listPrelude struct {
	sorted *storekit.ListSort
	limit  int
	where  []string
	args   []any
	arg    func(any) int
}

// buildListPrelude assembles it, or returns the first refusal — an
// out-of-vocabulary sort field, an unknown cf_ filter, an unreadable cursor.
func buildListPrelude(
	ctx context.Context,
	object string,
	fields map[string]string,
	active []fieldcatalog.Column,
	sort *string,
	limit *int,
	cursor *string,
	customFilters map[string]string,
) (*listPrelude, error) {
	sorted, err := storekit.ParseListSort(sort, storekit.SortVocabulary(fields, active))
	if err != nil {
		return nil, err
	}

	p := &listPrelude{sorted: sorted, limit: storekit.ClampLimit(limit), where: []string{offerTemplateWhereSeed}}
	p.arg = func(v any) int { p.args = append(p.args, v); return len(p.args) }

	// A row-scoped record narrows to what this reader may see. The
	// workspace-shared catalogues (products, offer templates) have no such
	// boundary — every seat sees the same rows — and asking for one is an
	// error rather than an empty clause, so they say so by passing "".
	if object != "" {
		scope, err := auth.ScopeClauseFor(ctx, object, "", p.arg)
		if err != nil {
			return nil, err
		}
		if scope != "" {
			p.where = append(p.where, scope)
		}
	}

	cfClauses, err := storekit.CustomFilterClauses(active, customFilters, p.arg)
	if err != nil {
		return nil, err
	}
	p.where = append(p.where, cfClauses...)

	if cursor != nil && *cursor != "" {
		clause, err := sorted.KeysetClause(*cursor, p.arg)
		if err != nil {
			return nil, err
		}
		p.where = append(p.where, clause)
	}
	return p, nil
}

// runListPage executes one prepared list read and turns it into a page:
// fetch limit+1 rows to learn whether another page exists, trim to the
// page, and encode the next cursor from the last row kept. Generic over
// the record type because the shape — not the record — is what repeats;
// `scan` and `key` are the only genuinely per-record parts.
func runListPage[T any](
	ctx context.Context,
	s *Store,
	pre *listPrelude,
	table, columns string,
	active []fieldcatalog.Column,
	where []string,
	scan func(pgx.Rows, []fieldcatalog.Column, *storekit.ListSort) ([]T, []*string, error),
	key func(T) (time.Time, ids.UUID),
) ([]T, storekit.Page, error) {
	var out []T
	var page storekit.Page
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+columns+storekit.SelectSuffix(active)+pre.sorted.CursorKeySuffix()+
				` FROM `+table+` WHERE `+strings.Join(where, " AND ")+
				pre.sorted.OrderBy()+storekit.SQLf(` LIMIT %d`, pre.limit+1),
			pre.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var cursorKeys []*string
		if out, cursorKeys, err = scan(rows, active, pre.sorted); err != nil {
			return err
		}
		if len(out) > pre.limit {
			out = out[:pre.limit]
			createdAt, id := key(out[len(out)-1])
			page = storekit.Page{
				HasMore:    true,
				NextCursor: pre.sorted.EncodePageCursor(cursorKeys[pre.limit-1], createdAt, id),
			}
		}
		return nil
	})
	if out == nil {
		out = []T{}
	}
	return out, page, err
}
