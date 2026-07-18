// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The person list read: the shared listPage runner bound to the person
// table — DM-VOCAB-1 sort vocabulary, the shared filter chain, and the
// person row scan + child attachment.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// personEntity is the person's auth object and table name.
const personEntity = "person"

// personNameColumn is the person's display column — the quick-find
// target and the DM-VOCAB-1 name sort key.
const personNameColumn = "full_name"

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
func (s *Store) ListPeople(ctx context.Context, in ListPeopleInput) ([]crmcontracts.Person, storekit.Page, error) {
	return listPage(ctx, s, in.Sort, in.Limit, listPageSpec[crmcontracts.Person]{
		entity:  personEntity,
		columns: personColumns,
		fields:  personListFields,
		filters: listFilters{
			IncludeArchived: in.IncludeArchived,
			OwnerID:         in.OwnerID,
			Query:           in.Query,
			Cursor:          in.Cursor,
			CustomFilters:   in.CustomFilters,
			nameColumn:      personNameColumn,
		}.clauses,
		scan:   scanPersonPage,
		attach: attachPersonChildren,
		cursorKey: func(last crmcontracts.Person) (time.Time, ids.UUID) {
			return last.CreatedAt, ids.UUID(last.Id)
		},
	})
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
