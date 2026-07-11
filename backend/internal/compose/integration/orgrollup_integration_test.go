// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The org hierarchy roll-up (GET /organizations/{id}/hierarchy-rollup,
// RD-T04): roll-up(node) = self(node) + Σ roll-up(readable child) over
// the parent_org_id tree. A child the caller cannot read contributes
// nothing and is disclosed by {id, display_name} only; all money
// converts to the workspace base currency, and a missing stored FX rate
// fails the whole read rather than inventing a rate.

import (
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// rollupStages is one dedicated pipeline with a known 40% open stage and
// a won stage, so every weighted expectation below is arithmetic the
// test controls rather than a value inherited from the workspace seed.
type rollupStages struct {
	pipeline, open, won ids.UUID
}

const rollupOpenWinProbability = 40

func seedRollupStages(t *testing.T, e *Env) rollupStages {
	t.Helper()
	st := rollupStages{pipeline: ids.NewV7(), open: ids.NewV7(), won: ids.NewV7()}
	e.WsExec(t, `INSERT INTO pipeline (id, workspace_id, name) VALUES ($1, $2, 'Rollup Pipeline')`,
		st.pipeline, e.WS)
	e.WsExec(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, $3, 'Qualified', 1, 'open', $4)`,
		st.open, e.WS, st.pipeline, rollupOpenWinProbability)
	e.WsExec(t, `INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability)
		VALUES ($1, $2, $3, 'Won', 2, 'won', 100)`,
		st.won, e.WS, st.pipeline)
	return st
}

// seedRollupOrg inserts one hierarchy node directly: the rollup is a
// read, so the audit/outbox write shape the people store would add is
// noise here, and parent_org_id wiring has no store-level entry point.
func seedRollupOrg(t *testing.T, e *Env, name string, owner, parent *ids.UUID) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	e.WsExec(t, `INSERT INTO organization (id, workspace_id, display_name, owner_id, parent_org_id, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, 'manual', 'human:test')`,
		id, e.WS, name, owner, parent)
	return id
}

// seedRollupOpenDeal attaches one open deal; a nil amount/currency pair
// seeds the honest half-empty deal the weighted sum must count as 0.
func seedRollupOpenDeal(t *testing.T, e *Env, st rollupStages, org ids.UUID, amountMinor *int64, currency *string) {
	t.Helper()
	e.WsExec(t, `INSERT INTO deal (id, workspace_id, name, amount_minor, currency, pipeline_id, stage_id, organization_id, status, source, captured_by)
		VALUES ($1, $2, 'Open Deal', $3, $4, $5, $6, $7, 'open', 'manual', 'human:test')`,
		ids.NewV7(), e.WS, amountMinor, currency, st.pipeline, st.open, org)
}

// seedRollupWonDeal closes a deal with the frozen FX rate the
// deal_closed_fx CHECK demands — the rate the quarter sum must reuse.
func seedRollupWonDeal(t *testing.T, e *Env, st rollupStages, org ids.UUID,
	amountMinor int64, currency, fxRateToBase string, closedAt time.Time,
) {
	t.Helper()
	e.WsExec(t, `INSERT INTO deal (id, workspace_id, name, amount_minor, currency, fx_rate_to_base, pipeline_id, stage_id, organization_id, status, closed_at, source, captured_by)
		VALUES ($1, $2, 'Won Deal', $3, $4, $5, $6, $7, $8, 'won', $9, 'manual', 'human:test')`,
		ids.NewV7(), e.WS, amountMinor, currency, fxRateToBase, st.pipeline, st.won, org, closedAt)
}

func seedRollupFxRate(t *testing.T, e *Env, fromCurrency, rate string, day time.Time) {
	t.Helper()
	e.WsExec(t, `INSERT INTO fx_rate (workspace_id, from_currency, to_currency, rate, rate_date)
		VALUES ($1, $2, 'EUR', $3, $4)`,
		e.WS, fromCurrency, rate, day)
}

func seedRollupOrgActivity(t *testing.T, e *Env, org ids.UUID, occurredAt time.Time) {
	t.Helper()
	activityID := ids.NewV7()
	e.WsExec(t, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'note', 'rollup touch', $3, 'manual', 'human:test')`,
		activityID, e.WS, occurredAt)
	e.WsExec(t, `INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
		VALUES ($1, $2, 'organization', $3)`,
		e.WS, activityID, org)
}

// rollupOrgReadPerms is the minimal caller the rollup admits: read on
// organization at the given row-scope tier, nothing else.
func rollupOrgReadPerms(scope principal.RowScope) principal.Permissions {
	return principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects:  map[string]principal.ObjectGrant{"organization": {Read: true}},
		RowScope: scope,
	}
}

func int64Ptr(v int64) *int64 { return &v }

// TestOrgRollupReconcilesTreeToSelves is the reconciliation invariant:
// the tree total equals the sum of every included node's scope=self
// figures, an empty node contributes a real 0 (and still counts in
// aggregated_account_count), a NULL-amount deal contributes 0, and a
// non-base-currency open deal converts at the stored as-of rate.
func TestOrgRollupReconcilesTreeToSelves(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	root := seedRollupOrg(t, e, "Root Co", nil, nil)
	childA := seedRollupOrg(t, e, "Child A", nil, &root)
	childB := seedRollupOrg(t, e, "Child B (empty)", nil, &root)
	grandchild := seedRollupOrg(t, e, "Grandchild", nil, &childA)
	now := time.Now().UTC()

	seedRollupOpenDeal(t, e, st, root, int64Ptr(100_000), strPtr("EUR"))
	seedRollupOpenDeal(t, e, st, root, nil, nil) // NULL amount: a real 0, never an error
	seedRollupOpenDeal(t, e, st, childA, int64Ptr(50_000), strPtr("EUR"))
	seedRollupOpenDeal(t, e, st, childA, int64Ptr(10_000), strPtr("USD")) // 0.5 → 5_000 base
	seedRollupOpenDeal(t, e, st, grandchild, int64Ptr(20_000), strPtr("EUR"))
	seedRollupFxRate(t, e, "USD", "0.5", now.AddDate(0, 0, -2))
	seedRollupWonDeal(t, e, st, root, 30_000, "EUR", "1.0", now)
	seedRollupOrgActivity(t, e, root, now.Add(-24*time.Hour))
	seedRollupOrgActivity(t, e, grandchild, now.Add(-24*time.Hour))
	seedRollupOrgActivity(t, e, childA, now.Add(-40*24*time.Hour)) // outside the 30d window

	tree, err := compose.OrgHierarchyRollup(e.Admin(), e.Pool, root, "tree")
	if err != nil {
		t.Fatalf("tree rollup: %v", err)
	}
	// Per-deal weighted at 40%: 40_000 (root) + 20_000 + 2_000 (childA) + 8_000 (grandchild).
	if tree.WeightedPipelineMinor != 70_000 {
		t.Errorf("tree weighted = %d, want 70000", tree.WeightedPipelineMinor)
	}
	if tree.ClosedWonMinor != 30_000 {
		t.Errorf("tree closed-won = %d, want 30000", tree.ClosedWonMinor)
	}
	if tree.ActivityCount30d != 2 {
		t.Errorf("tree activity count = %d, want 2 (the 40-day-old touch is out of window)", tree.ActivityCount30d)
	}
	if tree.AggregatedAccountCount != 4 {
		t.Errorf("aggregated account count = %d, want 4 (the empty sibling still counts)", tree.AggregatedAccountCount)
	}
	if tree.BaseCurrency != "EUR" {
		t.Errorf("base currency = %q, want EUR", tree.BaseCurrency)
	}
	if tree.RestrictedExcluded == nil || len(tree.RestrictedExcluded) != 0 {
		t.Errorf("restricted = %v, want non-nil empty", tree.RestrictedExcluded)
	}
	if tree.RootID != root || tree.Scope != "tree" || tree.ComputedAt.IsZero() {
		t.Errorf("result envelope = {%v %q %v}, want root id, tree scope, real computed_at",
			tree.RootID, tree.Scope, tree.ComputedAt)
	}

	var sumWeighted, sumWon int64
	var sumActivity, sumNodes int
	for _, node := range []ids.UUID{root, childA, childB, grandchild} {
		self, err := compose.OrgHierarchyRollup(e.Admin(), e.Pool, node, "self")
		if err != nil {
			t.Fatalf("self rollup of %v: %v", node, err)
		}
		if self.AggregatedAccountCount != 1 {
			t.Errorf("self count of %v = %d, want 1", node, self.AggregatedAccountCount)
		}
		if node == childB && (self.WeightedPipelineMinor != 0 || self.ClosedWonMinor != 0 || self.ActivityCount30d != 0) {
			t.Errorf("empty sibling self = %+v, want real zeros", self)
		}
		sumWeighted += self.WeightedPipelineMinor
		sumWon += self.ClosedWonMinor
		sumActivity += self.ActivityCount30d
		sumNodes += self.AggregatedAccountCount
	}
	if sumWeighted != tree.WeightedPipelineMinor || sumWon != tree.ClosedWonMinor ||
		sumActivity != tree.ActivityCount30d || sumNodes != tree.AggregatedAccountCount {
		t.Errorf("Σ(self) = {%d %d %d %d}, want the tree totals {%d %d %d %d}",
			sumWeighted, sumWon, sumActivity, sumNodes,
			tree.WeightedPipelineMinor, tree.ClosedWonMinor, tree.ActivityCount30d, tree.AggregatedAccountCount)
	}
}

// TestOrgRollupRestrictedNodeDisclosedAndGrantRestores: an unreadable
// child is excluded from every total and disclosed by identity only, its
// subtree is never visited (the ownerless — hence readable — grandchild
// is neither summed nor separately disclosed), and a live record_grant
// flips the child and its readable subtree back in on the next call.
func TestOrgRollupRestrictedNodeDisclosedAndGrantRestores(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	root := seedRollupOrg(t, e, "Root Co", &e.Rep1, nil)
	child := seedRollupOrg(t, e, "Restricted Child", &e.Rep3, &root)
	grandchild := seedRollupOrg(t, e, "Ownerless Grandchild", nil, &child)
	for _, org := range []ids.UUID{root, child, grandchild} {
		seedRollupOpenDeal(t, e, st, org, int64Ptr(10_000), strPtr("EUR"))
	}

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, rollupOrgReadPerms(principal.RowScopeOwn))
	pre, err := compose.OrgHierarchyRollup(rep, e.Pool, root, "tree")
	if err != nil {
		t.Fatalf("pre-grant rollup: %v", err)
	}
	if pre.WeightedPipelineMinor != 4_000 {
		t.Errorf("pre-grant weighted = %d, want 4000 (root only)", pre.WeightedPipelineMinor)
	}
	if pre.AggregatedAccountCount != 1 {
		t.Errorf("pre-grant account count = %d, want 1", pre.AggregatedAccountCount)
	}
	if len(pre.RestrictedExcluded) != 1 ||
		pre.RestrictedExcluded[0].ID != child || pre.RestrictedExcluded[0].DisplayName != "Restricted Child" {
		t.Fatalf("restricted = %+v, want exactly the child disclosed by id+name (grandchild never visited)",
			pre.RestrictedExcluded)
	}

	e.WsExec(t, `INSERT INTO record_grant (workspace_id, record_type, record_id, subject_type, subject_id, access, granted_by)
		VALUES ($1, 'organization', $2, 'user', $3, 'read', $3)`,
		e.WS, child, e.Rep1)

	post, err := compose.OrgHierarchyRollup(rep, e.Pool, root, "tree")
	if err != nil {
		t.Fatalf("post-grant rollup: %v", err)
	}
	if post.WeightedPipelineMinor != 12_000 {
		t.Errorf("post-grant weighted = %d, want 12000 (child + readable grandchild restored)", post.WeightedPipelineMinor)
	}
	if post.AggregatedAccountCount != 3 {
		t.Errorf("post-grant account count = %d, want 3", post.AggregatedAccountCount)
	}
	if len(post.RestrictedExcluded) != 0 {
		t.Errorf("post-grant restricted = %+v, want empty", post.RestrictedExcluded)
	}
}

// TestOrgRollupFXRateUnavailableFailsWholeRead: an open deal in a
// currency with no stored rate to base fails the WHOLE read with the
// typed error — never a partial sum, never a silent rate of 1.
func TestOrgRollupFXRateUnavailableFailsWholeRead(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	root := seedRollupOrg(t, e, "Root Co", nil, nil)
	seedRollupOpenDeal(t, e, st, root, int64Ptr(100_000), strPtr("EUR"))
	seedRollupOpenDeal(t, e, st, root, int64Ptr(10_000), strPtr("USD")) // no USD→EUR rate seeded

	_, err := compose.OrgHierarchyRollup(e.Admin(), e.Pool, root, "tree")
	var fxErr *compose.FXRateUnavailableError
	if !errors.As(err, &fxErr) {
		t.Fatalf("err = %v, want a typed FX-rate-unavailable failure", err)
	}
	if fxErr.Currency != "USD" || fxErr.AsOf.IsZero() {
		t.Errorf("fx error = %+v, want the missing currency and a real as-of instant", fxErr)
	}
}

// TestOrgRollupClosedWonQuarterWindow: closed-won counts only won deals
// whose closed_at falls in the current workspace-timezone quarter
// [start, end), converted at each deal's FROZEN rate — no fx_rate row
// exists here, so a live-rate lookup would fail the read instead.
func TestOrgRollupClosedWonQuarterWindow(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	root := seedRollupOrg(t, e, "Root Co", nil, nil)
	now := time.Now().UTC()
	seedRollupWonDeal(t, e, st, root, 10_000, "USD", "0.5", now)
	// 100 days back is outside any calendar quarter containing now (a
	// quarter spans at most 92 days), whatever today's date is.
	seedRollupWonDeal(t, e, st, root, 99_999, "USD", "1.0", now.AddDate(0, 0, -100))

	res, err := compose.OrgHierarchyRollup(e.Admin(), e.Pool, root, "tree")
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if res.ClosedWonMinor != 5_000 {
		t.Errorf("closed-won = %d, want 5000 (in-quarter deal at its frozen 0.5 rate only)", res.ClosedWonMinor)
	}
	if res.WeightedPipelineMinor != 0 {
		t.Errorf("weighted = %d, want 0 (won deals never re-enter the pipeline sum)", res.WeightedPipelineMinor)
	}
}

// TestOrgRollupRootGates: the threefold gate at the root — a missing
// object-read grant answers 403 before any row is touched; a nonexistent
// root and an out-of-scope root both answer 404, indistinguishable by
// design; an out-of-vocabulary scope is refused, not defaulted.
func TestOrgRollupRootGates(t *testing.T) {
	e := Setup(t)
	foreign := seedRollupOrg(t, e, "Foreign Org", &e.Rep3, nil)

	// Nonexistent root, unbounded admin: the tree walk itself must
	// answer not-found (the visibility gate has nothing to probe for an
	// unbounded caller).
	if _, err := compose.OrgHierarchyRollup(e.Admin(), e.Pool, ids.NewV7(), "tree"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("nonexistent root: err = %v, want not found", err)
	}

	// Rep1 sits in Team1; the root's owner Rep3 does not — out of scope
	// reads as not-there, never as an empty rollup.
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, rollupOrgReadPerms(principal.RowScopeTeam))
	if _, err := compose.OrgHierarchyRollup(rep, e.Pool, foreign, "tree"); !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("out-of-scope root: err = %v, want not found", err)
	}

	// RepPerms grants person/deal/pipeline but not organization: 403.
	noPerm := e.As(e.Rep1, []ids.UUID{e.Team1}, RepPerms)
	if _, err := compose.OrgHierarchyRollup(noPerm, e.Pool, foreign, "tree"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("no organization:read: err = %v, want permission denied", err)
	}

	// Permission refusal precedes input validation: a caller without
	// organization:read gets 403 even for a scope outside the vocabulary
	// — the bogus scope must never be judged before the grant is.
	if _, err := compose.OrgHierarchyRollup(noPerm, e.Pool, foreign, "subtree"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("no organization:read + bogus scope: err = %v, want permission denied", err)
	}

	// A scope outside {tree, self} is a refused input, not a default.
	if _, err := compose.OrgHierarchyRollup(e.Admin(), e.Pool, foreign, "subtree"); err == nil {
		t.Error("invalid scope accepted — must be refused")
	}
}

// TestOrgRollupSelfScopeSkipsPruning: scope=self returns the root's own
// figures without consulting child readability at all — unreadable
// children neither block the read nor appear as restricted.
func TestOrgRollupSelfScopeSkipsPruning(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	root := seedRollupOrg(t, e, "Root Co", &e.Rep1, nil)
	child := seedRollupOrg(t, e, "Hidden Child", &e.Rep3, &root)
	seedRollupOpenDeal(t, e, st, root, int64Ptr(10_000), strPtr("EUR"))
	seedRollupOpenDeal(t, e, st, child, int64Ptr(50_000), strPtr("EUR"))

	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, rollupOrgReadPerms(principal.RowScopeOwn))
	res, err := compose.OrgHierarchyRollup(rep, e.Pool, root, "self")
	if err != nil {
		t.Fatalf("self rollup: %v", err)
	}
	if res.WeightedPipelineMinor != 4_000 {
		t.Errorf("self weighted = %d, want 4000 (root's own deal only)", res.WeightedPipelineMinor)
	}
	if res.AggregatedAccountCount != 1 || res.Scope != "self" {
		t.Errorf("self envelope = {count %d, scope %q}, want {1, self}", res.AggregatedAccountCount, res.Scope)
	}
	if len(res.RestrictedExcluded) != 0 {
		t.Errorf("self restricted = %+v, want empty — self scope never prunes", res.RestrictedExcluded)
	}
}
