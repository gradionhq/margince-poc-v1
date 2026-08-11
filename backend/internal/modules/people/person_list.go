// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The person list read: the shared listPage runner bound to the person
// table — DM-VOCAB-1 sort vocabulary, the shared filter chain, and the
// person row scan + child attachment.

import (
	"context"
	"strings"
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
	// CapturedByKind filters on the captured_by prefix (ADR-0075/A121 §3a).
	CapturedByKind *string
	// AiWritten filters on whether an AI wrote into the record (§3a).
	AiWritten *bool
	// Sort is the contract's sort spec, validated against the core
	// vocabulary below plus the workspace's active cf_ columns.
	Sort *string
	// CustomFilters carries the request's cf_* query parameters —
	// equality matches against active custom columns (storekit listquery).
	CustomFilters map[string]string
	// Tag narrows to the people carrying one tag, by name. The tag
	// vocabulary belongs to another module, so this is a link predicate
	// rather than a column — see personTagClause.
	Tag *string
}

// personListFields is the person list's core sortable vocabulary —
// exactly the data-model §13.5 DM-VOCAB-1 set; active cf_ columns join
// it per request.
var personListFields = map[string]string{
	createdAtColumn:  storekit.KindTimestamp,
	updatedAtColumn:  storekit.KindTimestamp,
	personNameColumn: fieldcatalog.TypeText,
	ownerIDColumn:    storekit.KindUUID,
}

// personTagClause narrows the page to the people carrying one tag, or ""
// when the caller named none.
//
// Tags live in another module's tables, which this one may not import — but
// the predicate is SQL, and a tagged person is a row in `taggable` whatever
// package writes it. Matching is on the FOLDED name because that is what the
// vocabulary is unique by (`uq_tag_name` over lower(name)), which also means a
// name identifies at most one tag whether or not it has since been archived:
// the link is the fact this filter answers, and retiring the word from the
// picker does not un-tag the people who carry it. A record's own tag SECTION
// deliberately answers otherwise (compose/org360 shows live tags only):
// displaying a retired word beside a record is clutter, while failing to find
// the people who carry it is a wrong answer.
//
// EXISTS rather than a join: a person carries many tags, and a join would
// return them once per matching link — rows the keyset cursor would then page
// over as if they were distinct people. Both tables are workspace-qualified
// against the person's own workspace, which RLS already guarantees and
// `idx_taggable_entity` needs as its leading column.
func personTagClause(tag *string, arg func(any) int) string {
	if tag == nil {
		return ""
	}
	return storekit.SQLf(`EXISTS (
		SELECT 1 FROM taggable tg
		  JOIN tag t ON t.id = tg.tag_id AND t.workspace_id = tg.workspace_id
		WHERE tg.workspace_id = person.workspace_id
		  AND tg.entity_type = $%d AND tg.entity_id = person.id
		  AND lower(t.name) = $%d)`,
		arg(personEntity), arg(foldTagName(*tag)))
}

// foldTagName matches how the tag vocabulary is stored and compared: names
// are trimmed on write and unique under lower(), so a caller's spacing and
// case decide nothing about which tag they named.
func foldTagName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// ListPeople is the row-scoped person list read: quick-find, owner, tag and
// custom-field filters, keyset pagination under the validated sort.
func (s *Store) ListPeople(ctx context.Context, in ListPeopleInput) ([]crmcontracts.Person, storekit.Page, error) {
	shared := listFilters{
		IncludeArchived: in.IncludeArchived,
		CapturedByKind:  in.CapturedByKind,
		AiWritten:       in.AiWritten,
		entity:          personEntity,
		OwnerID:         in.OwnerID,
		Query:           in.Query,
		Cursor:          in.Cursor,
		CustomFilters:   in.CustomFilters,
		nameColumn:      personNameColumn,
	}
	return listPage(ctx, s, in.Sort, in.Limit, listPageSpec[crmcontracts.Person]{
		entity:  personEntity,
		columns: personColumns,
		fields:  personListFields,
		filters: func(active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error) {
			where, err := shared.clauses(active, sorted, arg)
			if err != nil {
				return nil, err
			}
			if clause := personTagClause(in.Tag, arg); clause != "" {
				where = append(where, clause)
			}
			return where, nil
		},
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
