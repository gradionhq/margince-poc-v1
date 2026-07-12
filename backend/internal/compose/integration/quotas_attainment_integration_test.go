// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The quota attainment read (RD-FORM-2/RD-WIRE-3): the golden-number
// reconciliation (Σ contributing == closed_won_minor, spec's worked
// 313,872.00-vs-280,000.00 EUR example), the two honest 422 refusals
// (zero target before any deal query, missing FX rate), owner/team deal
// scoping, the exclusion set (open, lost, archived, out-of-period, and
// NULL-base deals never count), target-currency conversion to the
// workspace base, and the archived-quota + RBAC postures.

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/quotas"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// attainmentClock pins the attainment store's injected clock mid-period
// (q1Start..q1End), so pace_pct and the FX as-of day are arithmetic the
// test controls rather than wall-clock reads.
var attainmentClock = time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)

func attainmentStore(e *Env) *quotas.Store {
	return quotas.NewStoreWithClock(e.Pool, func() time.Time { return attainmentClock })
}

// attainmentDealSeed is one deal row inserted directly — attainment is a
// read, so the audit/outbox write shape the deals store would add is
// noise here (the orgrollup suite's precedent).
type attainmentDealSeed struct {
	owner      ids.UUID
	amount     *int64  // nil seeds the honest NULL-base deal
	currency   *string // required by the 0050 money pair when amount is set
	fx         *string // numeric text; amount_minor_base = round(amount*fx) (0065)
	status     string  // won|lost|open
	closedAt   *time.Time
	lostReason *string
	archivedAt *time.Time
}

func seedAttainmentDeal(t *testing.T, e *Env, st rollupStages, d attainmentDealSeed) ids.UUID {
	t.Helper()
	stage := st.won
	if d.status == "open" {
		stage = st.open
	}
	id := ids.NewV7()
	e.WsExec(t, `INSERT INTO deal (id, workspace_id, name, owner_id, amount_minor, currency, fx_rate_to_base,
			pipeline_id, stage_id, status, closed_at, lost_reason, archived_at, source, captured_by)
		VALUES ($1, $2, 'Attainment Deal', $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 'manual', 'human:test')`,
		id, e.WS, d.owner, d.amount, d.currency, d.fx, st.pipeline, stage, d.status,
		d.closedAt, d.lostReason, d.archivedAt)
	return id
}

// seedWonDeal is the common case: a live won EUR-base deal at fx 1.0.
func seedWonDeal(t *testing.T, e *Env, st rollupStages, owner ids.UUID, amountMinor int64, closedAt time.Time) ids.UUID {
	t.Helper()
	eur, one := "EUR", "1.0000000000"
	return seedAttainmentDeal(t, e, st, attainmentDealSeed{
		owner: owner, amount: &amountMinor, currency: &eur, fx: &one,
		status: "won", closedAt: &closedAt,
	})
}

func TestQuotaAttainment_GoldenNumberReconciliation(t *testing.T) {
	// RD-AC-3's worked example: 313,872.00 EUR won against a 280,000.00
	// EUR target — every figure server-computed, decomposed per deal.
	e := Setup(t)
	st := seedRollupStages(t, e)
	store := attainmentStore(e)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 28000000))
	if err != nil {
		t.Fatal(err)
	}
	seedWonDeal(t, e, st, e.Rep1, 18000000, time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC))
	seedWonDeal(t, e, st, e.Rep1, 13387200, time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC))

	att, err := store.QuotaAttainment(ctx, ids.UUID(created.Id))
	if err != nil {
		t.Fatalf("attainment: %v", err)
	}
	if att.QuotaID != ids.UUID(created.Id) {
		t.Errorf("quota_id = %s, want %s", att.QuotaID, created.Id)
	}
	if att.ClosedWonMinor != 31387200 {
		t.Errorf("closed_won_minor = %d, want 31387200", att.ClosedWonMinor)
	}
	if att.TargetMinor != 28000000 || att.Currency != "EUR" {
		t.Errorf("target/currency = %d %s, want the base-currency target 28000000 EUR", att.TargetMinor, att.Currency)
	}
	if att.GapMinor != 3387200 {
		t.Errorf("gap_minor = %d, want +3387200 (+33,872.00 EUR)", att.GapMinor)
	}
	wantPct := 31387200.0 / 28000000.0 * 100
	if math.Abs(att.AttainmentPct-wantPct) > 0.01 {
		t.Errorf("attainment_pct = %v, want ≈%v", att.AttainmentPct, wantPct)
	}
	if att.Band != "met" {
		t.Errorf("band = %q, want met", att.Band)
	}
	wantPace := attainmentClock.Sub(q1Start).Seconds() / q1End.Sub(q1Start).Seconds() * 100
	if math.Abs(att.PacePct-wantPace) > 0.001 {
		t.Errorf("pace_pct = %v, want %v (the pinned mid-period clock)", att.PacePct, wantPace)
	}
	if !att.AsOfDate.Equal(attainmentClock) {
		t.Errorf("as_of_date = %v, want the pinned clock %v", att.AsOfDate, attainmentClock)
	}

	// The reconciliation invariant: the decomposition IS the total.
	if len(att.ContributingDeals) != 2 {
		t.Fatalf("contributing_deals = %d rows, want 2", len(att.ContributingDeals))
	}
	var sum int64
	for _, d := range att.ContributingDeals {
		sum += d.BaseValueMinor
	}
	if sum != att.ClosedWonMinor {
		t.Errorf("Σ contributing = %d, must equal closed_won_minor %d", sum, att.ClosedWonMinor)
	}
}

func TestQuotaAttainment_TargetZeroRefusedBeforeAnyDealQuery(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	store := attainmentStore(e)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 0))
	if err != nil {
		t.Fatal(err)
	}
	// A won deal that WOULD contribute — if the refusal arrived after the
	// deal query, contributing data would leak alongside the error.
	seedWonDeal(t, e, st, e.Rep1, 5000000, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))

	att, err := store.QuotaAttainment(ctx, ids.UUID(created.Id))
	if !errors.Is(err, quotas.ErrAttainmentTargetZero) {
		t.Fatalf("a zero target must answer ErrAttainmentTargetZero, got %v", err)
	}
	if att.ClosedWonMinor != 0 || len(att.ContributingDeals) != 0 {
		t.Errorf("the refusal must carry no contributing data, got %+v", att)
	}
}

func TestQuotaAttainment_MissingFxRateFailsHonestly(t *testing.T) {
	e := Setup(t)
	store := attainmentStore(e)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	// A USD target in an EUR-base workspace, with no USD→EUR rate on
	// file: the computation refuses rather than inventing a rate.
	in := ownerQuotaInput(e.Rep1, 10000000)
	in.Currency = "USD"
	created, err := store.CreateQuota(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QuotaAttainment(ctx, ids.UUID(created.Id)); !errors.Is(err, quotas.ErrAttainmentComputationFailed) {
		t.Fatalf("a missing FX rate must answer ErrAttainmentComputationFailed, got %v", err)
	}
}

func TestQuotaAttainment_CrossCurrencyTargetConvertsToBase(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	store := attainmentStore(e)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	// USD→EUR 0.9 on file before the pinned as-of day.
	seedRollupFxRate(t, e, "USD", "0.9000000000", attainmentClock.AddDate(0, 0, -1))

	in := ownerQuotaInput(e.Rep1, 10000000) // 100,000.00 USD
	in.Currency = "USD"
	created, err := store.CreateQuota(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// A USD won deal: amount_minor_base is ALREADY base-converted by the
	// 0065 GENERATED column (round(2,000,000 × 0.9) = 1,800,000) — the
	// attainment sum never converts a deal twice.
	usd, rate := "USD", "0.9000000000"
	amount := int64(2000000)
	closedAt := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	seedAttainmentDeal(t, e, st, attainmentDealSeed{
		owner: e.Rep1, amount: &amount, currency: &usd, fx: &rate,
		status: "won", closedAt: &closedAt,
	})

	att, err := store.QuotaAttainment(ctx, ids.UUID(created.Id))
	if err != nil {
		t.Fatalf("cross-currency attainment: %v", err)
	}
	if att.TargetMinor != 9000000 || att.Currency != "EUR" {
		t.Errorf("target = %d %s, want the base-converted 9000000 EUR (10M USD @ 0.9)", att.TargetMinor, att.Currency)
	}
	if att.ClosedWonMinor != 1800000 {
		t.Errorf("closed_won_minor = %d, want 1800000 (the deal's own frozen base value)", att.ClosedWonMinor)
	}
	if att.GapMinor != 1800000-9000000 {
		t.Errorf("gap_minor = %d, want %d — both sides of the gap in the base currency", att.GapMinor, 1800000-9000000)
	}
	if att.Band != "behind" {
		t.Errorf("band = %q, want behind (20%% attainment)", att.Band)
	}
}

func TestQuotaAttainment_TeamQuotaSumsMembersDeals(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	store := attainmentStore(e)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctx, quotas.CreateQuotaInput{
		TeamID: &e.Team1, PeriodStart: q1Start, PeriodEnd: q1End,
		TargetMinor: 10000000, Currency: "EUR",
	})
	if err != nil {
		t.Fatal(err)
	}
	inPeriod := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	seedWonDeal(t, e, st, e.Rep1, 3000000, inPeriod) // Team1 member
	seedWonDeal(t, e, st, e.Rep2, 4000000, inPeriod) // Team1 member
	seedWonDeal(t, e, st, e.Rep3, 9999999, inPeriod) // Team2 — never counts

	att, err := store.QuotaAttainment(ctx, ids.UUID(created.Id))
	if err != nil {
		t.Fatalf("team attainment: %v", err)
	}
	if att.ClosedWonMinor != 7000000 {
		t.Errorf("team closed_won_minor = %d, want 7000000 (the outsider's deal excluded)", att.ClosedWonMinor)
	}
	if len(att.ContributingDeals) != 2 {
		t.Errorf("contributing_deals = %d rows, want the two members' deals", len(att.ContributingDeals))
	}
}

func TestQuotaAttainment_ExclusionsAndPeriodBoundary(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	store := attainmentStore(e)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 10000000))
	if err != nil {
		t.Fatal(err)
	}

	// Counted: the period window is [period_start, period_end + 1 day) —
	// a close ON the end date still belongs to the period.
	seedWonDeal(t, e, st, e.Rep1, 1000000, q1Start)
	endDay := seedWonDeal(t, e, st, e.Rep1, 2000000, q1End.Add(6*time.Hour))

	// Excluded, one per reason.
	eur, one := "EUR", "1.0000000000"
	inPeriod := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	amount := int64(9999999)
	reason := "lost to rival"
	seedAttainmentDeal(t, e, st, attainmentDealSeed{ // still open
		owner: e.Rep1, amount: &amount, currency: &eur, status: "open",
	})
	seedAttainmentDeal(t, e, st, attainmentDealSeed{ // lost
		owner: e.Rep1, amount: &amount, currency: &eur, fx: &one,
		status: "lost", closedAt: &inPeriod, lostReason: &reason,
	})
	seedAttainmentDeal(t, e, st, attainmentDealSeed{ // archived won
		owner: e.Rep1, amount: &amount, currency: &eur, fx: &one,
		status: "won", closedAt: &inPeriod, archivedAt: &inPeriod,
	})
	dayAfterEnd := q1End.Add(24 * time.Hour)
	seedAttainmentDeal(t, e, st, attainmentDealSeed{ // out of period
		owner: e.Rep1, amount: &amount, currency: &eur, fx: &one,
		status: "won", closedAt: &dayAfterEnd,
	})
	nullBase := seedAttainmentDeal(t, e, st, attainmentDealSeed{ // NULL base: won, no amount
		owner: e.Rep1, status: "won", closedAt: &inPeriod,
	})

	att, err := store.QuotaAttainment(ctx, ids.UUID(created.Id))
	if err != nil {
		t.Fatalf("attainment: %v", err)
	}
	if att.ClosedWonMinor != 3000000 {
		t.Errorf("closed_won_minor = %d, want 3000000 (start + end-day deals only)", att.ClosedWonMinor)
	}
	if len(att.ContributingDeals) != 2 {
		t.Fatalf("contributing_deals = %d rows, want 2 — the NULL-base deal is omitted, not listed at 0", len(att.ContributingDeals))
	}
	for _, d := range att.ContributingDeals {
		if d.DealID == nullBase {
			t.Errorf("the NULL-base deal %s must be omitted from contributing_deals", nullBase)
		}
	}
	found := false
	for _, d := range att.ContributingDeals {
		found = found || d.DealID == endDay
	}
	if !found {
		t.Errorf("the deal closed on the period_end date must count (exclusive bound is end + 1 day)")
	}
}

func TestQuotaAttainment_ArchivedQuotaStillServesAndAbsentIsNotFound(t *testing.T) {
	e := Setup(t)
	st := seedRollupStages(t, e)
	store := attainmentStore(e)
	ctx := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(ctx, ownerQuotaInput(e.Rep1, 10000000))
	if err != nil {
		t.Fatal(err)
	}
	seedWonDeal(t, e, st, e.Rep1, 5000000, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if _, err := store.ArchiveQuota(ctx, ids.UUID(created.Id)); err != nil {
		t.Fatal(err)
	}

	// An archived quota is still fetchable by id (the house single-get
	// convention), so its attainment stays an honest, computable read.
	att, err := store.QuotaAttainment(ctx, ids.UUID(created.Id))
	if err != nil {
		t.Fatalf("attainment on an archived quota must still serve, got %v", err)
	}
	if att.ClosedWonMinor != 5000000 {
		t.Errorf("archived-quota closed_won_minor = %d, want 5000000", att.ClosedWonMinor)
	}

	if _, err := store.QuotaAttainment(ctx, ids.NewV7()); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("an unknown quota id must answer ErrNotFound, got %v", err)
	}
}

func TestQuotaAttainment_RequiresQuotaRead(t *testing.T) {
	e := Setup(t)
	store := attainmentStore(e)
	admin := e.As(e.Rep1, nil, quotaAdminPerms)

	created, err := store.CreateQuota(admin, ownerQuotaInput(e.Rep1, 10000000))
	if err != nil {
		t.Fatal(err)
	}
	// RepPerms carries deal.read but NO quota grant: seeing the sums a
	// quota decomposes still rides the quota object gate.
	noQuota := e.As(e.Rep2, []ids.UUID{e.Team1}, RepPerms)
	if _, err := store.QuotaAttainment(noQuota, ids.UUID(created.Id)); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a caller without quota.read must answer ErrPermissionDenied, got %v", err)
	}
}
