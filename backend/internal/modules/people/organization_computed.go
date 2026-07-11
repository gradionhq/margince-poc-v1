// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The RD-T08 formula-field display rows GetOrganization surfaces
// (RD-AC-6/RD-AC-7/RD-AC-N-1): one DB-computed row (open_pipeline, fed
// by the 0065 security_invoker view), plus four honest floor rows. The
// visibility gate is a pure in-memory permission check — the STATE-4
// absent-key case — never a database round trip.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// notYetBuiltReason floors a computed field with no backend data model
// at all. servedByHierarchyRollupReason is the honest alternative for
// weighted_pipeline: poc-v1, unlike the poc-1 reference this ports,
// already serves that figure — GET /organizations/{id}/hierarchy-rollup
// (arc 1b) — so "not_yet_built" would misstate the truth; the row still
// floors computable=false because it is not a DB-GENERATED artifact
// (RD-AC-6's own bar), it just isn't UNBUILT.
const (
	notYetBuiltReason             = "not_yet_built"
	servedByHierarchyRollupReason = "served_by_hierarchy_rollup"
	// awaitingFXReason floors open_pipeline when the view row EXISTS
	// (open deals reference this organization) but its aggregate is
	// itself NULL: every one of those deals is still missing
	// fx_rate_to_base, the ordinary state for an open deal (0065's
	// documented "not computable yet" case) — distinct from the
	// genuine zero of an organization with no open deals at all.
	awaitingFXReason       = "awaiting_fx"
	openPipelineFormulaSQL = "organization_open_pipeline_rollup (0065): SUM(deal.amount_minor_base) FROM deal WHERE deal.status = 'open' AND deal.organization_id = <this org> AND deal.archived_at IS NULL"
)

// openPipelineDependencies names the columns feeding the view's
// aggregate: the two inputs of deal.amount_minor_base's own GENERATED
// expression (0065), plus the two columns the view's WHERE clause
// gates participation on.
var openPipelineDependencies = []string{"deal.amount_minor", "deal.fx_rate_to_base", "deal.status", "deal.archived_at"}

// computedFieldsVisible answers the STATE-4 gate: does the acting
// principal's merged role policy grant computed_field:read? poc-1
// re-loaded role permissions from the database on every call
// (RollupStore.ComputedFieldsVisible); poc-v1's principal already
// carries its merged Permissions, resolved once at authentication
// (B-EP03.1), so this is a pure in-memory check — no query. The system
// principal (workspace provisioning, no role of its own) is trusted by
// construction, mirroring auth.Require's own carve-out; a request with
// no actor bound at all fails closed.
func computedFieldsVisible(ctx context.Context) bool {
	actor, ok := principal.Actor(ctx)
	if !ok {
		return false
	}
	if actor.Type == principal.PrincipalSystem {
		return true
	}
	return actor.Permissions.Allows("computed_field", principal.ActionRead)
}

// openPipelineRollup reads the organization_open_pipeline_rollup view
// (0065) for one organization, inside the SAME workspace transaction the
// caller (GetOrganization) already opened. The view is
// security_invoker=true (0065's own comment): it runs with the CALLING
// role's privileges and RLS, so the deal rows it sums are already
// scoped to the workspace the transaction's app.workspace_id GUC bound —
// the same tenant-isolation policy that gates a direct SELECT on deal.
//
// The poc-1 reference added a defense-in-depth join to
// organization.workspace_id here because it ran this read at pool
// level, outside any per-call GUC scope. That join would be redundant
// in this repo: the caller reaching this function has already run
// auth.EnsureVisible on the SAME organization id in the SAME
// transaction — itself RLS-gated on organization.workspace_id — so orgID
// is already proven to belong to the caller's own workspace before this
// query runs. Adding the join would only re-derive a fact the
// transaction already established, not add protection.
//
// No row (an organization with no open deals at all) is the honest
// "nothing to sum" case: (nil, 0, nil), never an error. dealCount is the
// caller's only way to distinguish that genuine-zero state from the
// OTHER honest "not computable yet" state — open deals exist
// (dealCount > 0) but every one is still missing fx_rate_to_base, so
// the aggregate itself comes back NULL.
func openPipelineRollup(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (minorBase *int64, dealCount int, err error) {
	err = tx.QueryRow(ctx,
		`SELECT open_pipeline_minor_base, open_deal_count
		 FROM organization_open_pipeline_rollup WHERE organization_id = $1`,
		orgID).Scan(&minorBase, &dealCount)
	if errors.Is(err, pgx.ErrNoRows) {
		// Scan never ran, so minorBase/dealCount are still their zero
		// values (nil, 0) — return the named vars, not literal zeroes:
		// the honest "nothing to sum" case above, not a swallowed error.
		return minorBase, dealCount, nil
	}
	if err != nil {
		return nil, 0, err
	}
	return minorBase, dealCount, nil
}

// organizationComputedFields assembles the 5 display rows RD-T08 names.
// It takes the view's two output columns (rule T8: no dead returns —
// openDealCount now has a real consumer, the three-way branch below).
//
// open_pipeline is a genuine three-way state, not a single floor:
//   - openDealCount == 0 (no view row: an organization with no open
//     deals at all) is the honest "nothing to sum" case: computable:true,
//     value_minor:0 — a real zero, not a missing one.
//   - openPipelineMinor != nil (the view row's aggregate is non-NULL) is
//     the genuinely computable case: computable:true, value_minor:sum.
//   - openPipelineMinor == nil AND openDealCount > 0 (open deals exist
//     but every one is still missing fx_rate_to_base, the ordinary state
//     for an open deal — 0065's documented "not computable yet" case) is
//     NOT a zero: flooring it would show a dishonest 0 pipeline beside a
//     non-zero weighted_pipeline. It floors instead to computable:false,
//     reason:"awaiting_fx", with no value_minor on the wire — the honest
//     "not computable yet" state 0065's migration comment already names,
//     now surfaced here instead of floored away. formula_sql stays
//     populated: the formula exists, only its FX input doesn't yet.
func organizationComputedFields(openPipelineMinor *int64, openDealCount int) []crmcontracts.ComputedField {
	weightedReason := servedByHierarchyRollupReason
	customerAgeReason := notYetBuiltReason
	nrrReason := notYetBuiltReason
	marginReason := notYetBuiltReason

	openPipelineRow := crmcontracts.ComputedField{
		Key:          "open_pipeline",
		Label:        "Open pipeline",
		Kind:         crmcontracts.ComputedFieldKindCurrencyMinor,
		FormulaSql:   openPipelineFormulaSQL,
		Dependencies: openPipelineDependencies,
	}
	switch {
	case openPipelineMinor != nil:
		value := *openPipelineMinor
		openPipelineRow.Computable = true
		openPipelineRow.ValueMinor = &value
	case openDealCount == 0:
		zero := int64(0)
		openPipelineRow.Computable = true
		openPipelineRow.ValueMinor = &zero
	default:
		reason := awaitingFXReason
		openPipelineRow.Computable = false
		openPipelineRow.Reason = &reason
	}

	return []crmcontracts.ComputedField{
		openPipelineRow,
		{
			Key:          "weighted_pipeline",
			Label:        "Weighted pipeline",
			Kind:         crmcontracts.ComputedFieldKindCurrencyMinor,
			FormulaSql:   "",
			Dependencies: []string{},
			Computable:   false,
			Reason:       &weightedReason,
		},
		{
			Key:          "customer_age",
			Label:        "Customer age",
			Kind:         crmcontracts.ComputedFieldKindDurationMonths,
			FormulaSql:   "",
			Dependencies: []string{},
			Computable:   false,
			Reason:       &customerAgeReason,
		},
		{
			Key:          "net_revenue_retention",
			Label:        "Net revenue retention",
			Kind:         crmcontracts.ComputedFieldKindPercent,
			FormulaSql:   "",
			Dependencies: []string{},
			Computable:   false,
			Reason:       &nrrReason,
		},
		{
			Key:          "blended_gross_margin",
			Label:        "Blended gross margin",
			Kind:         crmcontracts.ComputedFieldKindPercent,
			FormulaSql:   "",
			Dependencies: []string{},
			Computable:   false,
			Reason:       &marginReason,
		},
	}
}
