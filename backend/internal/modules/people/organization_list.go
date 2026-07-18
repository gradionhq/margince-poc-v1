// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The organization list read: DM-VOCAB-2 sort vocabulary, keyset
// pagination, row-scope + custom-field filtering (the
// relationship_list.go shape).

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
//
//nolint:dupl // deliberately parallel to ListPeople: a generic page-runner would abstract over the two record types for symmetry alone (ADR-0054: split for a reason, never symmetry)
func (s *Store) ListOrganizations(ctx context.Context, in ListOrganizationsInput) ([]crmcontracts.Organization, storekit.Page, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return nil, storekit.Page{}, err
	}
	active, err := s.activeColumns(ctx, "organization")
	if err != nil {
		return nil, storekit.Page{}, err
	}
	sorted, err := storekit.ParseListSort(in.Sort, storekit.SortVocabulary(organizationListFields, active))
	if err != nil {
		return nil, storekit.Page{}, err
	}
	limit := storekit.ClampLimit(in.Limit)

	where := []string{whereAlways}
	args := []any{}
	arg := func(v any) int { args = append(args, v); return len(args) }

	scope, err := auth.ScopeClauseFor(ctx, "organization", "", arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	if scope != "" {
		where = append(where, scope)
	}

	filters, err := organizationListFilters(in, active, sorted, arg)
	if err != nil {
		return nil, storekit.Page{}, err
	}
	where = append(where, filters...)

	var orgs []crmcontracts.Organization
	var page storekit.Page
	err = s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+orgColumns+storekit.SelectSuffix(active)+sorted.CursorKeySuffix()+
				` FROM organization WHERE `+strings.Join(where, " AND ")+
				sorted.OrderBy()+storekit.SQLf(` LIMIT %d`, limit+1),
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var cursorKeys []*string
		if orgs, cursorKeys, err = scanOrganizationPage(rows, active, sorted); err != nil {
			return err
		}
		if len(orgs) > limit {
			orgs = orgs[:limit]
			last := orgs[len(orgs)-1]
			page = storekit.Page{HasMore: true, NextCursor: sorted.EncodePageCursor(cursorKeys[limit-1], last.CreatedAt, ids.UUID(last.Id))}
		}
		return attachOrgDomains(ctx, tx, orgs)
	})
	if orgs == nil {
		orgs = []crmcontracts.Organization{}
	}
	return orgs, page, err
}

// organizationListFilters translates the request's optional filters into
// WHERE clauses, appending their arguments through arg — archived
// visibility, owner, classification, quick-find, custom-field equality,
// and the keyset cursor.
func organizationListFilters(in ListOrganizationsInput, active []fieldcatalog.Column, sorted *storekit.ListSort, arg func(any) int) ([]string, error) {
	var where []string
	if !in.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	if in.OwnerID != nil {
		where = append(where, storekit.SQLf("owner_id = $%d", arg(*in.OwnerID)))
	}
	if in.Classification != nil {
		where = append(where, storekit.SQLf("classification = $%d", arg(*in.Classification)))
	}
	if in.Query != nil && *in.Query != "" {
		where = append(where, storekit.QuickFindClause(arg(*in.Query), orgNameColumn))
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
