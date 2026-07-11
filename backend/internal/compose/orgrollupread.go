// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The gated aggregate read behind GET /organizations/{id}/
// hierarchy-rollup (RD-T04): one workspace transaction walking the org
// tree, pruning it to the caller's row scope, and computing the three
// measures over the included nodes. Lives in compose because the read
// spans organization, deal, stage, activity, and fx_rate — exactly the
// composition layer's charter; it durably owns none of them. The pure
// mechanics it builds on (prune, quarter bounds, rounding) live in
// orgrollup.go.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The rollup's scope vocabulary (crm.yaml enum): the whole readable
// subtree, or the root's own figures alone.
const (
	orgRollupScopeTree = "tree"
	orgRollupScopeSelf = "self"
)

// OrgRollupResult is the computed hierarchy roll-up for one root
// organization: every money figure in workspace base-currency minor
// units, plus the identity-only disclosure of pruned branches.
type OrgRollupResult struct {
	RootID                 ids.UUID
	Scope                  string
	WeightedPipelineMinor  int64
	ClosedWonMinor         int64
	BaseCurrency           string
	ActivityCount30d       int
	AggregatedAccountCount int
	RestrictedExcluded     []restrictedNode
	ComputedAt             time.Time
}

// OrgHierarchyRollup is the gated aggregate read: object-level read on
// organization, deal, AND activity, then — inside ONE workspace
// transaction — the root's row-scope visibility, the bounded tree walk,
// the per-node readability prune (tree scope only), and the three
// measures over the included nodes. Out-of-scope and nonexistent roots
// both answer ErrNotFound, indistinguishable by design.
//
// The rollup surfaces deal money and activity counts, so it demands the
// same object grants the forecast and activity reports do. Aggregation
// itself stays ORG-scoped: within a readable organization, per-deal and
// per-activity row visibility is deliberately not consulted, so account
// totals stay whole (the contract's description states the same policy).
//
// now is the read's injected clock (house shape: deals.CloseDateCorrector,
// approvals.Service, ai.meter) — the HTTP handler defaults it to time.Now
// at server construction, and a test pins a fixed instant so a quarter or
// 30-day window boundary can never flake between when it seeds and when
// the read evaluates "now".
func OrgHierarchyRollup(ctx context.Context, pool *pgxpool.Pool, rootID ids.UUID, scope string, now func() time.Time) (OrgRollupResult, error) {
	for _, object := range []datasource.EntityType{datasource.EntityOrganization, datasource.EntityDeal, datasource.EntityActivity} {
		if err := auth.Require(ctx, string(object), principal.ActionRead); err != nil {
			// Permission refusal precedes input validation: a caller missing
			// any of the three grants gets 403 even for a bogus scope value,
			// matching arc 1a's gate order — what the caller can't do is
			// decided before what the caller asked for is judged well-formed.
			return OrgRollupResult{}, err
		}
	}
	if scope != orgRollupScopeTree && scope != orgRollupScopeSelf {
		// The handler validates the enum at the edge; a value reaching
		// this far is refused with the wire-ready 422 shape rather than
		// silently defaulted (the contract names the vocabulary).
		return OrgRollupResult{}, httperr.Validation("scope", "invalid_enum",
			fmt.Sprintf("scope must be %q or %q", orgRollupScopeTree, orgRollupScopeSelf))
	}

	asOf := now().UTC()
	result := OrgRollupResult{RootID: rootID, Scope: scope, RestrictedExcluded: []restrictedNode{}, ComputedAt: asOf}
	err := database.WithWorkspaceTx(ctx, pool, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "organization", rootID); err != nil {
			return err
		}
		baseCurrency, loc, err := rollupWorkspaceMeta(ctx, tx)
		if err != nil {
			return err
		}
		nodes, err := loadOrgTree(ctx, tx, rootID)
		if err != nil {
			return err
		}
		if len(nodes) == 0 {
			// EnsureVisible skips the existence probe for unbounded
			// callers, so a nonexistent (or archived) root lands here.
			return fmt.Errorf("organization %s: %w", rootID, apperrors.ErrNotFound)
		}
		included, restricted, err := rollupIncludedNodes(ctx, tx, rootID, scope, nodes)
		if err != nil {
			return err
		}

		fx := &fxConverter{tx: tx, baseCurrency: baseCurrency, asOf: asOf, rates: map[string]pgtype.Numeric{}}
		if result.WeightedPipelineMinor, err = weightedPipelineMinor(ctx, tx, included, fx); err != nil {
			return err
		}
		if result.ClosedWonMinor, err = closedWonMinorThisQuarter(ctx, tx, included, asOf, loc); err != nil {
			return err
		}
		if result.ActivityCount30d, err = orgActivityCount30d(ctx, tx, included, asOf); err != nil {
			return err
		}
		result.BaseCurrency = baseCurrency
		result.AggregatedAccountCount = len(included)
		result.RestrictedExcluded = restricted
		return nil
	})
	if err != nil {
		return OrgRollupResult{}, err
	}
	return result, nil
}

// rollupWorkspaceMeta reads the workspace's base currency and reporting
// timezone. A stored zone the host cannot resolve degrades the quarter
// window to UTC rather than failing the read — the zone was validated
// at write time, so this only fires when the host lost its tzdata, and
// an aggregate read is the wrong place to surface that deployment fault.
func rollupWorkspaceMeta(ctx context.Context, tx pgx.Tx) (string, *time.Location, error) {
	var baseCurrency, tzName string
	if err := tx.QueryRow(ctx, `SELECT base_currency, timezone FROM workspace WHERE id = $1`,
		storekit.MustWorkspace(ctx)).Scan(&baseCurrency, &tzName); err != nil {
		return "", nil, fmt.Errorf("read workspace meta: %w", err)
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc = time.UTC
	}
	return baseCurrency, loc, nil
}

// orgRollupMaxDepth caps the recursive tree walk. The DB trigger already
// guarantees acyclicity, so this is a defensive belt against a
// pathologically deep (or corrupted) hierarchy, never a correctness rule.
const orgRollupMaxDepth = 50

// loadOrgTree walks the live organization hierarchy downward from
// rootID, root-first. RLS carries the workspace scope; an archived or
// missing root yields an empty slice for the caller to refuse.
//
// The walk recurses ONE level past orgRollupMaxDepth on purpose: a row
// at the cap depth proves the tree keeps going below what the rollup
// would sum, and a total that silently dropped those nodes would be a
// lie about money — the whole read fails loudly instead. (Seeding a
// 51-deep chain to exercise this in the integration lane would be all
// fixture and no insight; the sentinel row is plain SQL arithmetic.)
func loadOrgTree(ctx context.Context, tx pgx.Tx, rootID ids.UUID) ([]orgTreeNode, error) {
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE org_tree AS (
			SELECT o.id, o.parent_org_id, o.display_name, 0 AS depth
			FROM organization o
			WHERE o.id = $1 AND o.archived_at IS NULL
			UNION ALL
			SELECT o.id, o.parent_org_id, o.display_name, t.depth + 1
			FROM organization o
			JOIN org_tree t ON o.parent_org_id = t.id
			WHERE o.archived_at IS NULL AND t.depth + 1 <= $2
		)
		SELECT id, parent_org_id, display_name, depth FROM org_tree ORDER BY depth, id`,
		rootID, orgRollupMaxDepth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []orgTreeNode
	for rows.Next() {
		var n orgTreeNode
		var depth int
		if err := rows.Scan(&n.id, &n.parentID, &n.displayName, &depth); err != nil {
			return nil, err
		}
		if depth >= orgRollupMaxDepth {
			return nil, fmt.Errorf(
				"organization tree under %s exceeds the supported depth of %d; roll up from a lower node or flatten the hierarchy",
				rootID, orgRollupMaxDepth,
			)
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// rollupIncludedNodes resolves which tree nodes the totals may sum.
// scope=self is the root alone and consults no child readability at
// all; scope=tree prunes each branch at its first unreadable node,
// disclosing it by identity only.
func rollupIncludedNodes(ctx context.Context, tx pgx.Tx, rootID ids.UUID, scope string,
	nodes []orgTreeNode,
) ([]ids.UUID, []restrictedNode, error) {
	if scope == orgRollupScopeSelf {
		return []ids.UUID{rootID}, []restrictedNode{}, nil
	}
	readable, err := orgReadablePredicate(ctx, tx, nodes)
	if err != nil {
		return nil, nil, err
	}
	included, restricted, rootReadable := pruneUnreadable(rootID, nodes, readable)
	if !rootReadable {
		// EnsureVisible vouched for the root in this same transaction;
		// disagreeing here would be a scope-clause defect — refuse with
		// the same existence-hiding answer rather than sum anything.
		return nil, nil, fmt.Errorf("organization %s: %w", rootID, apperrors.ErrNotFound)
	}
	return included, restricted, nil
}

// orgReadablePredicate answers which of the tree's nodes pass the
// caller's row scope, resolved in ONE batch query over the house
// visibility predicate. An unbounded caller reads everything, so no
// query is spent proving it.
func orgReadablePredicate(ctx context.Context, tx pgx.Tx, nodes []orgTreeNode) (func(ids.UUID) bool, error) {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	clause, err := auth.ScopeClauseFor(ctx, "organization", "", arg)
	if err != nil {
		return nil, err
	}
	if clause == "" {
		return func(ids.UUID) bool { return true }, nil
	}

	nodeIDs := make([]ids.UUID, len(nodes))
	for i, n := range nodes {
		nodeIDs[i] = n.id
	}
	rows, err := tx.Query(ctx,
		fmt.Sprintf(`SELECT id FROM organization WHERE id = ANY($%d) AND %s`, arg(nodeIDs), clause),
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	readable := make(map[ids.UUID]bool, len(nodes))
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		readable[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return func(id ids.UUID) bool { return readable[id] }, nil
}

// fxConverter converts open-deal amounts to the workspace base currency
// at the stored as-of rate, memoizing one lookup per currency for the
// duration of a single rollup read. Rates stay pgtype.Numeric end to
// end: the conversion is exact decimal arithmetic, the same discipline
// closed-won gets from Postgres ROUND over numeric.
type fxConverter struct {
	tx           pgx.Tx
	baseCurrency string
	asOf         time.Time
	rates        map[string]pgtype.Numeric
}

// toBase converts amountMinor from currency to the base currency. A
// currency with no stored rate on or before the as-of day fails the
// whole read with the typed error — a partial sum or a silent rate of 1
// would be a lie about money.
func (c *fxConverter) toBase(ctx context.Context, amountMinor int64, currency string) (int64, error) {
	if currency == c.baseCurrency {
		return amountMinor, nil
	}
	rate, ok := c.rates[currency]
	if !ok {
		// The as-of day is the UTC calendar date, matching fx_rate's
		// one-rate-per-pair-per-UTC-day grain; the text bind + cast
		// keeps the comparison independent of the session timezone.
		err := c.tx.QueryRow(ctx, `
			SELECT rate FROM fx_rate
			WHERE from_currency = $1 AND to_currency = $2 AND rate_date <= $3::date
			ORDER BY rate_date DESC
			LIMIT 1`,
			currency, c.baseCurrency, c.asOf.Format(time.DateOnly)).Scan(&rate)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, &FXRateUnavailableError{Currency: currency, AsOf: c.asOf}
		}
		if err != nil {
			return 0, err
		}
		c.rates[currency] = rate
	}
	return convertToBase(amountMinor, rate)
}

// openDealRow is one open deal's contribution inputs: nullable money
// (a deal may honestly have no amount yet) and its stage's live win
// probability.
type openDealRow struct {
	amountMinor    *int64
	currency       *string
	winProbability int
}

// weightedPipelineMinor sums round(base(amount) × win_probability/100)
// per open deal over the included organizations (formulas §6: round per
// deal, then sum, so the total reconciles exactly to its parts). Rows
// are collected before converting because the FX lookup queries the
// same transaction's connection.
func weightedPipelineMinor(ctx context.Context, tx pgx.Tx, included []ids.UUID, fx *fxConverter) (int64, error) {
	// The stage join deliberately carries NO archived_at filter, matching
	// the forecast report's join: archiving a stage reshapes the pipeline
	// vocabulary, it must never silently zero the open deals still in it.
	rows, err := tx.Query(ctx, `
		SELECT d.amount_minor, d.currency, s.win_probability
		FROM deal d
		JOIN stage s ON s.id = d.stage_id AND s.workspace_id = d.workspace_id
		WHERE d.organization_id = ANY($1) AND d.status = 'open' AND d.archived_at IS NULL`,
		included)
	if err != nil {
		return 0, err
	}
	deals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (openDealRow, error) {
		var d openDealRow
		err := row.Scan(&d.amountMinor, &d.currency, &d.winProbability)
		return d, err
	})
	if err != nil {
		return 0, err
	}

	var total int64
	for _, d := range deals {
		if d.amountMinor == nil {
			continue // an amountless deal contributes a real 0, never an error
		}
		// The deal_amount_currency_pair CHECK guarantees a non-null
		// amount carries its currency.
		baseMinor, err := fx.toBase(ctx, *d.amountMinor, *d.currency)
		if err != nil {
			return 0, err
		}
		weighted, err := weightedValue(baseMinor, d.winProbability)
		if err != nil {
			return 0, err
		}
		total += weighted
	}
	return total, nil
}

// closedWonMinorThisQuarter sums won deals closed in the current
// workspace-timezone quarter [start, end). amount_minor_base (0065) IS
// round(amount_minor * fx_rate_to_base)::bigint, computed once at write
// time from each deal's FROZEN close-time rate — never a live lookup;
// reading the GENERATED column here is the same figure as recomputing
// the product, just not re-derived per read. No FX failure can arise
// here: the deal_closed_fx CHECK guarantees fx_rate_to_base for every
// closed deal that has an amount, and an amountless won deal's NULL
// amount_minor_base is skipped by SUM — an honest 0, not an invented rate.
func closedWonMinorThisQuarter(ctx context.Context, tx pgx.Tx, included []ids.UUID, asOf time.Time, loc *time.Location) (int64, error) {
	start, end := currentQuarterBounds(asOf, loc)
	var total int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(d.amount_minor_base), 0)::bigint
		FROM deal d
		WHERE d.organization_id = ANY($1) AND d.status = 'won' AND d.archived_at IS NULL
		  AND d.closed_at >= $2 AND d.closed_at < $3`,
		included, start, end).Scan(&total)
	return total, err
}

// orgActivityCount30d counts distinct live activities linked to any
// included organization in the half-open window [asOf−30d, asOf).
// DISTINCT because one activity may link several orgs of the same tree
// and must count once; the upper bound keeps a future-dated activity (a
// scheduled call, a clock-skewed import) out of a count that claims to
// describe the PAST 30 days.
func orgActivityCount30d(ctx context.Context, tx pgx.Tx, included []ids.UUID, asOf time.Time) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `
		SELECT COUNT(DISTINCT a.id)
		FROM activity a
		JOIN activity_link l ON l.activity_id = a.id AND l.entity_type = 'organization'
		WHERE l.organization_id = ANY($1) AND a.archived_at IS NULL
		  AND a.occurred_at >= $2 AND a.occurred_at < $3`,
		included, asOf.AddDate(0, 0, -30), asOf).Scan(&count)
	return count, err
}
