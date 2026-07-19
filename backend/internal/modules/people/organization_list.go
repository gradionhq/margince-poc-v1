// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The organization list read: the shared listPage runner bound to the
// organization table — DM-VOCAB-2 sort vocabulary, the shared filter
// chain plus the classification filter, and the organization row scan +
// domain attachment.

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/fieldcatalog"
)

// organizationEntity is the organization's auth object and table name.
const organizationEntity = "organization"

// orgNameColumn is the organization's display column — the quick-find
// target and the DM-VOCAB-2 name sort key.
const orgNameColumn = "display_name"

// ListOrganizationsInput carries the organization list's contract
// parameters.
type ListOrganizationsInput struct {
	Cursor          *string
	Limit           *int
	Query           *string
	OwnerID         *ids.UserID
	Classification  *string
	IncludeArchived bool
	// Sort is the contract's sort spec, validated against the core
	// vocabulary below plus the workspace's active cf_ columns.
	Sort *string
	// CustomFilters carries the request's cf_* query parameters —
	// equality matches against active custom columns (storekit listquery).
	CustomFilters map[string]string
}

// organizationListFields is the organization list's core sortable
// vocabulary — exactly the data-model §13.5 DM-VOCAB-2 set; active cf_
// columns join it per request.
var organizationListFields = map[string]string{
	"created_at":  storekit.KindTimestamp,
	"updated_at":  storekit.KindTimestamp,
	orgNameColumn: fieldcatalog.TypeText,
	ownerIDColumn: storekit.KindUUID,
}

// ListOrganizations is the row-scoped organization list read:
// quick-find, owner, classification and custom-field filters, keyset
// pagination under the validated sort.
func (s *Store) ListOrganizations(ctx context.Context, in ListOrganizationsInput) ([]crmcontracts.Organization, storekit.Page, error) {
	shared := listFilters{
		IncludeArchived: in.IncludeArchived,
		OwnerID:         in.OwnerID,
		Query:           in.Query,
		Cursor:          in.Cursor,
		CustomFilters:   in.CustomFilters,
		nameColumn:      orgNameColumn,
	}
	return listPage(ctx, s, in.Sort, in.Limit, listPageSpec[crmcontracts.Organization]{
		entity:  organizationEntity,
		columns: orgColumns,
		fields:  organizationListFields,
		filters: func(active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error) {
			where, err := shared.clauses(active, sorted, arg)
			if err != nil {
				return nil, err
			}
			if in.Classification != nil {
				where = append(where, storekit.SQLf("classification = $%d", arg(*in.Classification)))
			}
			return where, nil
		},
		scan:   scanOrganizationPage,
		attach: attachOrgDomains,
		cursorKey: func(last crmcontracts.Organization) (time.Time, ids.UUID) {
			return last.CreatedAt, ids.UUID(last.Id)
		},
	})
}

// scanOrganizationPage drains one list query's rows: each organization
// plus, under a non-default sort, the row's cursor key (the trailing
// __cursor_key column CursorKeySuffix appended).
func scanOrganizationPage(rows pgx.Rows, active []fieldcatalog.Column, sorted *storekit.ListSort) ([]crmcontracts.Organization, []*string, error) {
	var orgs []crmcontracts.Organization
	var cursorKeys []*string
	for rows.Next() {
		var key *string
		extra := []any{}
		if sorted != nil {
			extra = append(extra, &key)
		}
		o, err := scanOrganization(rows, active, extra...)
		if err != nil {
			return nil, nil, err
		}
		orgs = append(orgs, o)
		cursorKeys = append(cursorKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return orgs, cursorKeys, nil
}
