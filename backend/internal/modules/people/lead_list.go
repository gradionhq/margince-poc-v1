// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The lead list read: the shared listPage runner bound to the lead table,
// so the list orders by the request's validated sort rather than always by
// creation time. Score is the field this matters most for — a lead list is
// read to find the warmest rows, and a score column that cannot order them
// is a number the reader has to scan for by eye.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// leadEntity is the lead's auth object and table name.
const leadEntity = "lead"

// The lead's sortable columns, named once. The vocabulary below and the
// clauses that read them agree by construction rather than by two people
// spelling the same column the same way.
const (
	// leadNameColumn is the display column: the quick-find target and the
	// name sort key.
	leadNameColumn    = "full_name"
	leadCompanyColumn = "company_name"
	leadStatusColumn  = "status"
	leadScoreColumn   = "score"
	createdAtColumn   = "created_at"
	updatedAtColumn   = "updated_at"
)

// leadListFields is the lead list's core sortable vocabulary. Every column
// the list surface shows is here, so a header the reader can click is a
// header the server can answer; active cf_ columns join it per request.
var leadListFields = map[string]string{
	createdAtColumn:   storekit.KindTimestamp,
	updatedAtColumn:   storekit.KindTimestamp,
	leadNameColumn:    fieldcatalog.TypeText,
	leadCompanyColumn: fieldcatalog.TypeText,
	leadStatusColumn:  fieldcatalog.TypeText,
	leadScoreColumn:   fieldcatalog.TypeNumber,
	ownerIDColumn:     storekit.KindUUID,
}

// ListLeads is the row-scoped lead list read: quick-find, the status and
// owner filters, and keyset pagination under the validated sort.
func (s *Store) ListLeads(ctx context.Context, in ListLeadsInput) ([]crmcontracts.Lead, storekit.Page, error) {
	return listPage(ctx, s, in.Sort, in.Limit, listPageSpec[crmcontracts.Lead]{
		entity:  leadEntity,
		columns: leadColumns,
		fields:  leadListFields,
		filters: func(active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error) {
			where, err := listFilters{
				IncludeArchived: in.IncludeArchived,
				CapturedByKind:  in.CapturedByKind,
				AiWritten:       in.AiWritten,
				entity:          leadEntity,
				OwnerID:         in.OwnerID,
				Query:           in.Query,
				Cursor:          in.Cursor,
				nameColumn:      leadNameColumn,
			}.clauses(active, sorted, arg)
			if err != nil {
				return nil, err
			}
			// The lead's own narrowing, alongside the shared chain.
			if in.Status != nil {
				where = append(where, storekit.SQLf(leadStatusColumn+" = $%d", arg(*in.Status)))
			}
			return where, nil
		},
		scan: scanLeadPage,
		// A lead is one flat row: no child tables to load alongside the page.
		attach: func(context.Context, pgx.Tx, []crmcontracts.Lead) error { return nil },
		cursorKey: func(last crmcontracts.Lead) (time.Time, ids.UUID) {
			return last.CreatedAt, ids.UUID(last.Id)
		},
	})
}

// scanLeadPage drains one list query's rows: each lead plus, under a
// non-default sort, the row's trailing __cursor_key.
func scanLeadPage(rows pgx.Rows, active []fieldcatalog.Column, sorted *storekit.ListSort) ([]crmcontracts.Lead, []*string, error) {
	var leads []crmcontracts.Lead
	var cursorKeys []*string
	for rows.Next() {
		var key *string
		extra := []any{}
		if sorted != nil {
			extra = append(extra, &key)
		}
		l, err := scanLead(rows, active, extra...)
		if err != nil {
			return nil, nil, err
		}
		leads = append(leads, l)
		cursorKeys = append(cursorKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return leads, cursorKeys, nil
}
