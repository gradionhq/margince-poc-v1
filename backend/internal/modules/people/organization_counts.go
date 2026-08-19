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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// attachOrgCounts fills contact_count and open_deal_count for a page.
//
// contact_count is a count of the contacts the CALLER may see, under the
// same row-scope predicate the person list applies: owner-only capture
// privacy holds for a count as for a name, since a number that moves when a
// colleague captures a private contact discloses that contact.
//
// open_deal_count counts the WHOLE workspace — the same population the
// account's computed_fields open-pipeline row sums — by founder decision
// (PO-EXT-10, 2026-08-18): a pipeline figure on an account is a fact about
// the account, not about who may open each deal. What it does follow is that
// row's visibility gate (STATE-4): a role without computed_field:read is
// shown no count of a pipeline it may not see. Absent means withheld; every
// visible account carries a number, zero included, so a reader can tell
// "none" from "not yours to know".
func attachOrgCounts(ctx context.Context, tx pgx.Tx, orgs []crmcontracts.Organization) error {
	if len(orgs) == 0 {
		return nil
	}
	idx := make(map[openapi_types.UUID]*crmcontracts.Organization, len(orgs))
	orgIDs := make([]ids.UUID, len(orgs))
	// The object grant comes first, as it does on the two lists themselves: a
	// role that may not read people gets no contact count, and one that may
	// not read deals gets no deal count — absent, not zero.
	contactsVisible := grantVisible(ctx, "person")
	dealsVisible := grantVisible(ctx, "deal") && computedFieldsVisible(ctx)
	for i := range orgs {
		idx[orgs[i].Id] = &orgs[i]
		orgIDs[i] = ids.UUID(orgs[i].Id)
		if contactsVisible {
			zero := 0
			orgs[i].ContactCount = &zero
		}
		if dealsVisible {
			zeroDeals := 0
			orgs[i].OpenDealCount = &zeroDeals
		}
	}
	if contactsVisible {
		if err := fillContactCounts(ctx, tx, idx, orgIDs); err != nil {
			return err
		}
	}
	if !dealsVisible {
		return nil
	}
	return fillOpenDealCounts(ctx, tx, idx, orgIDs)
}

// grantVisible is the object-grant half of a read, answered from the
// principal's merged permissions the way computedFieldsVisible answers its
// own: no query, system principal trusted, no actor fails closed.
func grantVisible(ctx context.Context, object string) bool {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return false
	}
	if actor.Type == principal.PrincipalSystem {
		return true
	}
	return actor.Permissions.Allows(object, principal.ActionRead)
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
		   AND `+CurrentPrimaryEmploymentSQL("rel")+`
		   AND rel.archived_at IS NULL`+scope+`
		 GROUP BY rel.organization_id`, args,
		func(o *crmcontracts.Organization, n int) { o.ContactCount = &n })
}

// fillOpenDealCounts reads the 0065 organization_open_pipeline_rollup view
// for the page — the ONE spelling of "open deal" this module reads, so the
// list's count and the company page's open-pipeline tile derive from the
// same rows and cannot disagree.
func fillOpenDealCounts(ctx context.Context, tx pgx.Tx, idx map[openapi_types.UUID]*crmcontracts.Organization, orgIDs []ids.UUID) error {
	return fillCount(ctx, tx, idx,
		`SELECT organization_id, open_deal_count
		 FROM organization_open_pipeline_rollup
		 WHERE organization_id = ANY($1)`, []any{orgIDs},
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
