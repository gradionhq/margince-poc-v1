// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The two roll-ups every company row shows (PO-EXT-10, AC-companies-2/3):
// how many people work here, and how many deals are open. Attached to a
// page in one query each, the same batch shape attachOrgDomains uses, so a
// list of fifty accounts costs two statements rather than a hundred.

import (
	"context"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// attachOrgCounts fills contact_count and open_deal_count for a page.
//
// Both are counts of what the CALLER may see, under the same row-scope
// predicate the person and deal lists apply. A count is a read: an
// owner-private contact or another team's deal must not surface as "one
// more" on an account the caller happens to share, since a number that
// moves when a hidden record is created discloses that record.
//
// open_deal_count also follows the computed_fields gate the single read
// applies (STATE-4): a role without computed_field:read is shown no count
// of a pipeline it may not see. Absent means withheld; every visible
// account carries a number, zero included, so a reader can tell "none"
// from "not yours to know".
func attachOrgCounts(ctx context.Context, tx pgx.Tx, orgs []crmcontracts.Organization) error {
	if len(orgs) == 0 {
		return nil
	}
	idx := make(map[openapi_types.UUID]*crmcontracts.Organization, len(orgs))
	orgIDs := make([]ids.UUID, len(orgs))
	dealsVisible := computedFieldsVisible(ctx)
	for i := range orgs {
		idx[orgs[i].Id] = &orgs[i]
		orgIDs[i] = ids.UUID(orgs[i].Id)
		zero := 0
		orgs[i].ContactCount = &zero
		if dealsVisible {
			zeroDeals := 0
			orgs[i].OpenDealCount = &zeroDeals
		}
	}
	if err := fillContactCounts(ctx, tx, idx, orgIDs); err != nil {
		return err
	}
	if !dealsVisible {
		return nil
	}
	return fillOpenDealCounts(ctx, tx, idx, orgIDs)
}

// fillContactCounts counts current-primary employment edges to live people
// the caller may see. The organization end needs no predicate of its own:
// every account on the page already passed the list's row scope.
func fillContactCounts(ctx context.Context, tx pgx.Tx, idx map[openapi_types.UUID]*crmcontracts.Organization, orgIDs []ids.UUID) error {
	args := []any{orgIDs}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := auth.ScopeClauseFor(ctx, "person", "p", arg)
	if err != nil {
		return err
	}
	if scope != "" {
		scope = " AND " + scope
	}
	return fillCount(ctx, tx, idx,
		`SELECT rel.organization_id, count(*)
		 FROM relationship rel
		 JOIN person p ON p.id = rel.person_id AND p.archived_at IS NULL
		 WHERE rel.organization_id = ANY($1)
		   AND rel.kind = 'employment'
		   AND rel.is_current_primary
		   AND rel.archived_at IS NULL`+scope+`
		 GROUP BY rel.organization_id`, args,
		func(o *crmcontracts.Organization, n int) { o.ContactCount = &n })
}

// fillOpenDealCounts counts open, live deals the caller may see. It spells
// "open deal" exactly as the 0065 organization_open_pipeline_rollup view
// does (status = 'open', not archived) but reads the deal table directly,
// because the view carries no row-scope predicate and a count must.
func fillOpenDealCounts(ctx context.Context, tx pgx.Tx, idx map[openapi_types.UUID]*crmcontracts.Organization, orgIDs []ids.UUID) error {
	args := []any{orgIDs}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := auth.ScopeClauseFor(ctx, "deal", "d", arg)
	if err != nil {
		return err
	}
	if scope != "" {
		scope = " AND " + scope
	}
	return fillCount(ctx, tx, idx,
		`SELECT d.organization_id, count(*)
		 FROM deal d
		 WHERE d.organization_id = ANY($1)
		   AND d.status = 'open'
		   AND d.archived_at IS NULL`+scope+`
		 GROUP BY d.organization_id`, args,
		func(o *crmcontracts.Organization, n int) { o.OpenDealCount = &n })
}

// fillCount runs one (organization_id, count) query and hands each row's
// count to set on the matching organization.
func fillCount(ctx context.Context, tx pgx.Tx, idx map[openapi_types.UUID]*crmcontracts.Organization,
	query string, args []any, set func(*crmcontracts.Organization, int),
) error {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var orgID ids.UUID
		var n int
		if err := rows.Scan(&orgID, &n); err != nil {
			return err
		}
		if o, ok := idx[openapi_types.UUID(orgID)]; ok {
			set(o, n)
		}
	}
	return rows.Err()
}
