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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// attachOrgCounts fills contact_count and open_deal_count for a page.
//
// contact_count counts current-primary employment edges to live people. It
// is a fact about the account rather than about any one person, which is
// why it is not narrowed by the caller's person row scope: an employer name
// the caller cannot read already leaves the edge itself visible, and a
// count of edges says no more than that.
//
// open_deal_count follows the computed_fields gate the single read applies
// (STATE-4): a role without computed_field:read is shown no count of a
// pipeline it may not see. Absent means withheld; every visible account
// carries a number, zero included, so a reader can tell "none" from "not
// yours to know".
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
	if err := fillCount(ctx, tx, idx,
		`SELECT rel.organization_id, count(*)
		 FROM relationship rel
		 JOIN person p ON p.id = rel.person_id AND p.archived_at IS NULL
		 WHERE rel.organization_id = ANY($1)
		   AND rel.kind = 'employment'
		   AND rel.is_current_primary
		   AND rel.archived_at IS NULL
		 GROUP BY rel.organization_id`, orgIDs,
		func(o *crmcontracts.Organization, n int) { o.ContactCount = &n }); err != nil {
		return err
	}
	if !dealsVisible {
		return nil
	}
	// The 0065 view is the ONE spelling of "open deal" this module reads;
	// the single read's open-pipeline row derives from the same rows, so
	// the list and the page cannot disagree about the count.
	return fillCount(ctx, tx, idx,
		`SELECT organization_id, open_deal_count
		 FROM organization_open_pipeline_rollup
		 WHERE organization_id = ANY($1)`, orgIDs,
		func(o *crmcontracts.Organization, n int) { o.OpenDealCount = &n })
}

// fillCount runs one (organization_id, count) query and hands each row's
// count to set on the matching organization.
func fillCount(ctx context.Context, tx pgx.Tx, idx map[openapi_types.UUID]*crmcontracts.Organization,
	query string, orgIDs []ids.UUID, set func(*crmcontracts.Organization, int),
) error {
	rows, err := tx.Query(ctx, query, orgIDs)
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
