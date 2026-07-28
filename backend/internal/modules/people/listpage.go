// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The ONE list page-read for this module's record lists (person,
// organization): RBAC + row-scope, DM-VOCAB sort validation over core +
// active cf_ columns, the shared optional-filter chain, keyset
// pagination with the limit+1 probe, and per-type child attachment.
// person_list.go / organization_list.go each bind one listPageSpec —
// what varies is data (table, vocabulary, scan, attach), not the read.

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// whereAlways seeds the AND chain so every filter appends uniformly —
// the chain is never empty even when no filter applies.
const whereAlways = "1=1"

// listPageSpec binds one record type into listPage. entity doubles as
// the auth object and the table name — the module's record tables are
// named after their objects.
type listPageSpec[T any] struct {
	entity  string
	columns string
	// fields is the core sortable vocabulary (data-model §13.5); active
	// cf_ columns join it per request.
	fields map[string]string
	// filters appends the request's optional WHERE clauses (their
	// arguments through arg) — typically listFilters.clauses plus any
	// type-specific extras.
	filters func(active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error)
	// scan drains one page's rows into records plus, under a non-default
	// sort, each row's trailing __cursor_key.
	scan func(rows pgx.Rows, active []fieldcatalog.Column, sorted *storekit.ListSort) ([]T, []*string, error)
	// attach loads the page's child rows (emails/phones, domains) in the
	// same transaction as the page read.
	attach func(ctx context.Context, tx pgx.Tx, recs []T) error
	// cursorKey exposes the last record's keyset identity for the
	// next-page cursor.
	cursorKey func(last T) (time.Time, ids.UUID)
}

// listPage is the shared list read every spec runs through.
func listPage[T any](ctx context.Context, s *Store, sortSpec *string, limitIn *int, spec listPageSpec[T]) ([]T, storekit.Page, error) {
	if err := auth.Require(ctx, spec.entity, principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumns(ctx, spec.entity)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	sorted, err := storekit.ParseListSort(sortSpec, storekit.SortVocabulary(spec.fields, active))
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(limitIn)

	where := []string{whereAlways}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClauseFor(ctx, spec.entity, "", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	filters, err := spec.filters(active, sorted, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	where = append(where, filters...)

	var recs []T
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+spec.columns+storekit.SelectSuffix(active)+sorted.CursorKeySuffix()+
				` FROM `+spec.entity+` WHERE `+strings.Join(where, " AND ")+
				sorted.OrderBy()+storekit.SQLf(` LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		var cursorKeys []*string
		if recs, cursorKeys, err = spec.scan(rows, active, sorted); err != nil {
			return err
		}
		if len(recs) > limit {
			recs = recs[:limit]
			createdAt, id := spec.cursorKey(recs[len(recs)-1])
			page = storekit.Page{HasMore: true, NextCursor: sorted.EncodePageCursor(cursorKeys[limit-1], createdAt, id)}
		}
		return spec.attach(ctx, tx, recs)
	})
	if recs == nil {
		recs = []T{}
	}
	return recs, page, err
}

// listFilters is the optional-filter set the record lists share; each
// list's contract input maps onto it, and type-specific extras (e.g. the
// organization classification) append alongside in the spec's filters.
type listFilters struct {
	IncludeArchived bool
	OwnerID         *ids.UserID
	Query           *string
	Cursor          *string
	CustomFilters   map[string]string
	// CapturedByKind filters on WHO created the row, matched against the
	// captured_by prefix. `agent` is the review list for the records an AI
	// created (ADR-0075/A121 §3a). The value is checked against the closed enum
	// by capturedByKindArg at the handler — declaring it in the contract does
	// not enforce it on the wire.
	CapturedByKind *string
	// nameColumn is the quick-find target — the record's display column.
	nameColumn string
}

// capturedByKindClause is the ONE spelling of the provenance filter
// (ADR-0075/A121 §3a). It lives outside listFilters because the lead list
// builds its own WHERE chain rather than using that struct, and two copies of
// "which prefix counts as an AI" is exactly how the person list and the lead
// list end up disagreeing about what the review list contains.
//
// The whole LIKE pattern is ONE bound argument, never concatenated into the
// SQL. The contract's enum values are plain ASCII words carrying no LIKE
// metacharacter today; binding the pattern is what keeps that true of a value
// the enum gains later.
func capturedByKindClause(kind *string, arg func(any) int) (string, bool) {
	if kind == nil || *kind == "" {
		return "", false
	}
	return storekit.SQLf("captured_by LIKE $%d", arg(*kind+":%")), true
}

// clauses translates the filters into WHERE clauses, appending their
// arguments through arg — archived visibility, owner, provenance,
// quick-find, custom-field equality, and the keyset cursor.
func (f listFilters) clauses(active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error) {
	var where []string
	if !f.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if f.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*f.OwnerID)))
	}
	if clause, ok := capturedByKindClause(f.CapturedByKind, arg); ok {
		where = append(where, clause)
	}
	if f.Query != nil && *f.Query != "" {
		where = append(where, storekit.QuickFindClause(arg(*f.Query), f.nameColumn))
	}
	cfClauses, err := storekit.CustomFilterClauses(active, f.CustomFilters, arg)
	if err != nil {
		return nil, err
	}
	where = append(where, cfClauses...)
	if f.Cursor != nil && *f.Cursor != "" {
		clause, err := sorted.KeysetClause(*f.Cursor, arg)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
	}
	return where, nil
}

// capturedByKindValid refuses a provenance kind outside the contract's enum.
//
// This is NOT belt-and-braces over the transport. Binding a query parameter
// whose schema is a string enum only checks that it is a string — nothing at
// the wire layer rejects an unknown value. Without this check a typo like
// `captured_by_kind=ai` builds the clause `captured_by LIKE 'ai:%'`, matches
// nothing, and answers 200 with an empty page, which reads exactly like "no AI
// created anything here". A filter whose failure mode is a confident wrong
// answer is worse than no filter.
//
// Each list operation generates its own string kind for the parameter, so the
// check is generic over the kind and closes over that type's own Valid().
func capturedByKindValid[T ~string](v *T, valid func(T) bool) error {
	if v == nil || valid(*v) {
		return nil
	}
	return httperr.Validation("captured_by_kind", "invalid",
		"must be one of human, agent, connector, system")
}

// capturedByKindArg maps the optional provenance parameter onto the store
// input, once capturedByKindValid has accepted it.
func capturedByKindArg[T ~string](v *T) *string {
	if v == nil {
		return nil
	}
	s := string(*v)
	return &s
}
