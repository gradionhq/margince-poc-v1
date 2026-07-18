// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The person list read: DM-VOCAB-1 sort vocabulary, keyset pagination,
// row-scope + custom-field filtering (the relationship_list.go shape).

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// personNameColumn is the person's display column — the quick-find
// target and the DM-VOCAB-1 name sort key.
const personNameColumn = "full_name"

// whereAlways seeds the AND chain so every filter appends uniformly —
// the chain is never empty even when no filter applies.
const whereAlways = "1=1"

// ListPeopleInput carries the person list's contract parameters.
type ListPeopleInput struct {
	Cursor          *string
	Limit           *int
	Query           *string
	OwnerID         *ids.UserID
	IncludeArchived bool
	// Sort is the contract's sort spec, validated against the core
	// vocabulary below plus the workspace's active cf_ columns.
	Sort *string
	// CustomFilters carries the request's cf_* query parameters —
	// equality matches against active custom columns (storekit listquery).
	CustomFilters map[string]string
}

// personListFields is the person list's core sortable vocabulary —
// exactly the data-model §13.5 DM-VOCAB-1 set; active cf_ columns join
// it per request.
var personListFields = map[string]string{
	"created_at":     storekit.KindTimestamp,
	"updated_at":     storekit.KindTimestamp,
	personNameColumn: fieldcatalog.TypeText,
	ownerIDColumn:    storekit.KindUUID,
}

// ListPeople is the row-scoped person list read: quick-find, owner and
// custom-field filters, keyset pagination under the validated sort.
//
//nolint:dupl // deliberately parallel to ListOrganizations: a generic page-runner would abstract over the two record types for symmetry alone (ADR-0054: split for a reason, never symmetry)
func (s *Store) ListPeople(ctx context.Context, in ListPeopleInput) ([]crmcontracts.Person, storekit.Page, error) {
	if err := auth.Require(ctx, "person", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumns(ctx, "person")
	if err != nil {
		return nil, storekit.Page{}, err
	}
	sorted, err := storekit.ParseListSort(in.Sort, storekit.SortVocabulary(personListFields, active))
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{whereAlways}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClauseFor(ctx, "person", "", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	filters, err := personListFilters(in, active, sorted, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	where = append(where, filters...)

	var people []crmcontracts.Person
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+personColumns+storekit.SelectSuffix(active)+sorted.CursorKeySuffix()+
				` FROM person WHERE `+strings.Join(where, " AND ")+
				sorted.OrderBy()+storekit.SQLf(` LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		var cursorKeys []*string
		if people, cursorKeys, err = scanPersonPage(rows, active, sorted); err != nil {
			return err
		}
		if len(people) > limit {
			people = people[:limit]
			last := people[len(people)-1]
			page = storekit.Page{HasMore: true, NextCursor: sorted.EncodePageCursor(cursorKeys[limit-1], last.CreatedAt, ids.UUID(last.Id))}
		}
		return attachPersonChildren(ctx, tx, people)
	})
	if people == nil {
		people = []crmcontracts.Person{}
	}
	return people, page, err
}

// personListFilters translates the request's optional filters into WHERE
// clauses, appending their arguments through arg — archived visibility,
// owner, quick-find, custom-field equality, and the keyset cursor.
func personListFilters(in ListPeopleInput, active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error) {
	var where []string
	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, storekit.QuickFindClause(arg(*in.Query), personNameColumn))
	}
	cfClauses, err := storekit.CustomFilterClauses(active, in.CustomFilters, arg)
	if err != nil {
		return nil, err
	}
	where = append(where, cfClauses...)
	if in.Cursor != nil && *in.Cursor != "" {
		clause, err := sorted.KeysetClause(*in.Cursor, arg)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
	}
	return where, nil
}

// scanPersonPage drains one list query's rows: each person plus, under a
// non-default sort, the row's cursor key (the trailing __cursor_key
// column CursorKeySuffix appended).
func scanPersonPage(rows pgx.Rows, active []fieldcatalog.Column, sorted *storekit.ListSort) ([]crmcontracts.Person, []*string, error) {
	var people []crmcontracts.Person
	var cursorKeys []*string
	for rows.Next() {
		var key *string
		extra := []any{}
		if sorted != nil {
			extra = append(extra, &key)
		}
		p, err := scanPerson(rows, active, extra...)
		if err != nil {
			return nil, nil, err
		}
		people = append(people, p)
		cursorKeys = append(cursorKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return people, cursorKeys, nil
}
