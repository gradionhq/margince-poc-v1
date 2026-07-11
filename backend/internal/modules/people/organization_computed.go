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
	openPipelineFormulaSQL        = "organization_open_pipeline_rollup (0065): SUM(deal.amount_minor_base) FROM deal WHERE deal.status = 'open' AND deal.organization_id = <this org> AND deal.archived_at IS NULL"
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
// "nothing to sum" case: (nil, 0, nil), never an error.
func openPipelineRollup(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (minorBase *int64, err error) {
	var dealCount int // scanned from the view but unused for display: the schema carries
	// it for future rows; no consumer today per RD-T08 (rule T8).
	err = tx.QueryRow(ctx,
		`SELECT open_pipeline_minor_base, open_deal_count
		 FROM organization_open_pipeline_rollup WHERE organization_id = $1`,
		orgID).Scan(&minorBase, &dealCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return minorBase, nil
}

// organizationComputedFields assembles the 5 display rows RD-T08 names.
// It takes only the aggregate figure; the count from the view (rule T8:
// no dead returns) stays at the SQL layer until a consumer claims it.
//
// A NULL aggregate (open deals exist but every one is still missing its
// FX input, so amount_minor_base is itself NULL — 0065's documented
// "not computable yet" case) and NO row at all (no open deals) both
// floor to a real value_minor of 0 here, with computable staying true:
// the poc-1-tested behaviour. 0065's migration comment records the more
// nuanced "NULL is honestly not-computable-yet" distinction at the SQL
// layer; this display row deliberately takes the coarser floor because
// a record-page tile has no way to render "unknown" money, and 0 is the
// truthful lower bound of "the open deals we CAN price sum to this much."
func organizationComputedFields(openPipelineMinor *int64) []crmcontracts.ComputedField {
	value := int64(0)
	if openPipelineMinor != nil {
		value = *openPipelineMinor
	}
	weightedReason := servedByHierarchyRollupReason
	customerAgeReason := notYetBuiltReason
	nrrReason := notYetBuiltReason
	marginReason := notYetBuiltReason

	return []crmcontracts.ComputedField{
		{
			Key:          "open_pipeline",
			Label:        "Open pipeline",
			Kind:         crmcontracts.ComputedFieldKindCurrencyMinor,
			ValueMinor:   &value,
			FormulaSql:   openPipelineFormulaSQL,
			Dependencies: openPipelineDependencies,
			Computable:   true,
		},
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
