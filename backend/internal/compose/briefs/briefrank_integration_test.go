// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package briefs

// The Morning-Brief deterministic spine over real rows (B-E05.1/.3b/
// .12/.13): a fixed seed + fixed clock reproduces a known queue; the
// run round-trips through the brief_run/brief_item read model; acted
// and dismissed marks drop the deal from the next run until it
// materially changes; and the candidate set is bounded by the caller's
// own row scope — a rep's brief never ranks a deal they cannot see.

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/compose/integration"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// briefClock is the fixed generation instant of the first run; marks and
// the second run happen at fixed later instants.
var briefClock = time.Date(2026, 6, 4, 6, 0, 0, 0, time.UTC)

// briefEnv is the seeded brief fixture: three open deals shaped after
// the §10.1 worked examples (warmth omitted — no stakeholders seeded, so
// the factor sits honestly at its floor).
type briefEnv struct {
	*integration.Env
	engine      *BriefEngine
	dealA       ids.UUID // 80% win, €60k, closes in 5 days, one linked activity
	dealB       ids.UUID // 25% win, no amount, closes in 200 days, no activity
	dealC       ids.UUID // 10% win, no amount, closes in 200 days — below the bar
	activityOnA ids.UUID
	pipeline    ids.PipelineID
	stageA      ids.UUID
	repCtx      context.Context
}

func setupBrief(t *testing.T) *briefEnv {
	t.Helper()
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)
	pipeline, _, _ := integration.DealFixture(t, e)

	// Three open stages with the worked examples' win probabilities.
	stages, err := collectIDList(owner.Query(context.Background(), `
		SELECT id FROM stage WHERE pipeline_id = $1 AND semantic = 'open' ORDER BY position`, pipeline))
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) < 3 {
		t.Fatalf("default pipeline seeds %d open stages, the fixture needs 3", len(stages))
	}
	for i, win := range []int{80, 25, 10} {
		if _, err := owner.Exec(context.Background(),
			`UPDATE stage SET win_probability = $2 WHERE id = $1`, stages[i], win); err != nil {
			t.Fatal(err)
		}
	}

	b := &briefEnv{
		Env:      e,
		engine:   NewBriefEngine(e.Pool, e.People),
		pipeline: pipeline,
		stageA:   stages[0],
		repCtx:   e.As(e.Rep1, []ids.UUID{e.Team1}, integration.AdminPerms),
	}
	b.dealA = b.seedBriefDeal(t, owner, "Deal A", stages[0], int64Ptr(60_000_00), closeOn(briefClock, 5), &e.Rep1)
	b.dealB = b.seedBriefDeal(t, owner, "Deal B", stages[1], nil, closeOn(briefClock, 200), &e.Rep1)
	b.dealC = b.seedBriefDeal(t, owner, "Deal C", stages[2], nil, closeOn(briefClock, 200), &e.Rep1)

	// Deal A's overnight change: one linked activity before the clock
	// (no previous run → the overnight window is all-time).
	b.activityOnA = integration.SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'email', 'reply arrived', '2026-06-04T01:00:00Z', 'manual', 'human:x')`, e.WS)
	integration.LinkActivity(t, owner, e.WS, b.activityOnA, "deal", b.dealA)
	return b
}

// closeOn returns the expected-close date the given days after the
// clock's UTC day.
func closeOn(clock time.Time, days int) *time.Time {
	d := clock.UTC().Truncate(24*time.Hour).AddDate(0, 0, days)
	return &d
}

// seedBriefDeal creates an open deal and pins its rank inputs directly
// (amounts in the workspace base currency, so no FX row is needed).
func (b *briefEnv) seedBriefDeal(t *testing.T, owner *pgx.Conn, name string, stage ids.UUID, amountMinor *int64, close *time.Time, ownerID *ids.UUID) ids.UUID {
	t.Helper()
	in := deals.CreateDealInput{
		Name: name, PipelineID: b.pipeline, StageID: ids.From[ids.StageKind](stage), Source: "manual",
	}
	if ownerID != nil {
		oid := ids.From[ids.UserKind](*ownerID)
		in.OwnerID = &oid
	}
	d, err := b.Deals.CreateDeal(b.Admin(), in)
	if err != nil {
		t.Fatal(err)
	}
	id := ids.UUID(d.Id)
	var currency *string
	if amountMinor != nil {
		eur := "EUR"
		currency = &eur
	}
	if _, err := owner.Exec(context.Background(), `
		UPDATE deal SET amount_minor = $2, currency = $3, expected_close_date = $4 WHERE id = $1`,
		id, amountMinor, currency, close); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBriefRankReproducesAKnownQueueOnAFixedSeedAndClock(t *testing.T) {
	b := setupBrief(t)

	ranking, err := b.engine.Rank(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}

	// Fewer than ten deals carry an amount → the fallback norm; €60k caps
	// the revenue factor at 1.0.
	if ranking.RevenueNormMinor != briefRevenueNormFallbackMinor {
		t.Fatalf("revenue norm = %d, want the %d fallback below ten deals of history", ranking.RevenueNormMinor, briefRevenueNormFallbackMinor)
	}
	if ranking.CandidateCount != 2 || len(ranking.Queue) != 2 {
		t.Fatalf("queue/candidates = %d/%d, want 2/2 (Deal C below the bar, never padded in)", len(ranking.Queue), ranking.CandidateCount)
	}
	if ranking.Queue[0].DealID != b.dealA || ranking.Queue[1].DealID != b.dealB {
		t.Fatalf("queue = %v, want [Deal A, Deal B]", queueDeals(ranking.Queue))
	}

	wantA := BriefFeatureVector{Winnability: 0.80, Revenue: 1.0, Timing: 1.0, Momentum: 1.0, Warmth: 0}
	if ranking.Queue[0].Features != wantA {
		t.Fatalf("Deal A features = %+v, want %+v", ranking.Queue[0].Features, wantA)
	}
	if got, want := ranking.Queue[0].Composite, wantA.composite(); math.Abs(got-want) > 1e-9 {
		t.Fatalf("Deal A composite = %.6f, want %.6f", got, want)
	}
	wantB := BriefFeatureVector{Winnability: 0.25, Revenue: 0, Timing: 0.2, Momentum: 0.4, Warmth: 0}
	if ranking.Queue[1].Features != wantB {
		t.Fatalf("Deal B features = %+v, want %+v", ranking.Queue[1].Features, wantB)
	}

	// Evidence resolves to real rows: Deal A carries its deal row plus
	// the overnight activity; Deal B (all factors at floors) its deal row.
	if len(ranking.Queue[0].EvidenceIDs) != 2 ||
		ranking.Queue[0].EvidenceIDs[0] != b.dealA || ranking.Queue[0].EvidenceIDs[1] != b.activityOnA {
		t.Fatalf("Deal A evidence = %v, want [deal, overnight activity]", ranking.Queue[0].EvidenceIDs)
	}
	if len(ranking.Queue[1].EvidenceIDs) != 1 || ranking.Queue[1].EvidenceIDs[0] != b.dealB {
		t.Fatalf("Deal B evidence = %v, want [deal]", ranking.Queue[1].EvidenceIDs)
	}

	// Determinism: the same seed + clock reproduces the same queue.
	again, err := b.engine.Rank(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Queue) != len(ranking.Queue) {
		t.Fatalf("second pass queue length %d != %d", len(again.Queue), len(ranking.Queue))
	}
	for i := range again.Queue {
		if again.Queue[i].DealID != ranking.Queue[i].DealID || again.Queue[i].Composite != ranking.Queue[i].Composite {
			t.Fatalf("second pass diverged at %d: %v vs %v", i, again.Queue[i], ranking.Queue[i])
		}
	}
}

// Warmth rides the injected people §4 seam: with an engaged stakeholder
// the factor equals the seam's own answer and the contributing
// interactions join the evidence.
func TestBriefWarmthComesFromTheStrengthSeam(t *testing.T) {
	b := setupBrief(t)
	owner := integration.OwnerConn(t)
	stakeholder := integration.SeedStakeholder(t, b.Env, owner, b.dealA, "inbound", "outbound")

	strength, err := b.People.PersonStrength(b.repCtx, ids.From[ids.PersonKind](stakeholder), briefClock)
	if err != nil {
		t.Fatal(err)
	}
	if strength.Strength == 0 {
		t.Fatal("fixture broken: the stakeholder must have a nonzero §4 strength")
	}

	ranking, err := b.engine.Rank(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	itemA := ranking.Queue[0]
	if itemA.DealID != b.dealA {
		t.Fatalf("queue head = %s, want Deal A", itemA.DealID)
	}
	if want := float64(strength.Strength) / 100; itemA.Features.Warmth != want {
		t.Fatalf("warmth = %v, want the seam's %v", itemA.Features.Warmth, want)
	}
	evidence := map[ids.UUID]bool{}
	for _, id := range itemA.EvidenceIDs {
		evidence[id] = true
	}
	for _, id := range strength.ContributingIDs {
		if !evidence[id.UUID] {
			t.Fatalf("warmth evidence %s missing from the item's evidence %v", id, itemA.EvidenceIDs)
		}
	}
}

func TestBriefSnapshotRoundTripsTheReadModel(t *testing.T) {
	b := setupBrief(t)
	owner := integration.OwnerConn(t)

	if _, err := b.engine.LatestRun(b.repCtx); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("latest run before any snapshot → %v, want ErrNotFound", err)
	}

	run, err := b.engine.SnapshotRun(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	read, err := b.engine.LatestRun(b.repCtx)
	if err != nil {
		t.Fatal(err)
	}
	if read.ID != run.ID || !read.AsOf.Equal(briefClock) || read.CandidateCount != 2 ||
		read.RevenueNormMinor != briefRevenueNormFallbackMinor || read.UserID != b.Rep1 {
		t.Fatalf("read-back run = %+v, want the snapshot %+v", read, run)
	}
	if len(read.Items) != 2 {
		t.Fatalf("read-back items = %d, want 2", len(read.Items))
	}
	for i, item := range read.Items {
		want := run.Items[i]
		if item.ID != want.ID || item.DealID != want.DealID || item.Rank != i+1 ||
			item.Composite != want.Composite || item.Features != want.Features ||
			item.State != "new" || item.StateAt != nil {
			t.Fatalf("read-back item %d = %+v, want %+v", i, item, want)
		}
		if len(item.EvidenceIDs) == 0 {
			t.Fatalf("read-back item %d lost its evidence — evidence-or-omit fails on the round trip", i)
		}
	}

	// The snapshot is audited in the same transaction (write shape).
	var audits int
	if err := owner.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_log WHERE entity_type = 'brief_run' AND entity_id = $1 AND action = 'create'`,
		run.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("brief_run create audit rows = %d, want 1", audits)
	}

	// Another rep has no view into this run: their latest is not-found,
	// not their colleague's brief.
	rep2 := b.As(b.Rep2, []ids.UUID{b.Team1}, integration.AdminPerms)
	if _, err := b.engine.LatestRun(rep2); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("rep2's latest run → %v, want ErrNotFound (a brief is personal)", err)
	}
}

// Marking is owner-only, audited, and never a silent overwrite.
func TestBriefMarksAreOwnerOnlyAuditedAndConflictSafe(t *testing.T) {
	b := setupBrief(t)
	owner := integration.OwnerConn(t)

	run, err := b.engine.SnapshotRun(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	itemA := run.Items[0]
	markAt := briefClock.Add(2 * time.Hour)

	// Only the run's owner may mark: another rep sees not-found.
	rep2 := b.As(b.Rep2, []ids.UUID{b.Team1}, integration.AdminPerms)
	if _, err := b.engine.MarkActed(rep2, itemA.ID, markAt); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("foreign mark → %v, want ErrNotFound (existence-hiding)", err)
	}

	acted, err := b.engine.MarkActed(b.repCtx, itemA.ID, markAt)
	if err != nil {
		t.Fatal(err)
	}
	if acted.State != "acted" || acted.StateAt == nil || !acted.StateAt.Equal(markAt) {
		t.Fatalf("acted item = %+v, want state acted at %s", acted, markAt)
	}

	// A second mark is a conflict, never a silent overwrite.
	if _, err := b.engine.MarkDismissed(b.repCtx, itemA.ID, markAt.Add(time.Minute)); !errors.Is(err, apperrors.ErrConflict) {
		t.Fatalf("double mark → %v, want ErrConflict", err)
	}

	// The mark is audited.
	var audits int
	if err := owner.QueryRow(context.Background(), `
		SELECT count(*) FROM audit_log WHERE entity_type = 'brief_item' AND entity_id = $1 AND action = 'update'`,
		itemA.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("brief_item mark audit rows = %d, want 1", audits)
	}
}

func TestBriefActedAndDismissedItemsLeaveTheNextQueue(t *testing.T) {
	b := setupBrief(t)
	owner := integration.OwnerConn(t)

	run, err := b.engine.SnapshotRun(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	markAt := briefClock.Add(2 * time.Hour)
	if _, err := b.engine.MarkActed(b.repCtx, run.Items[0].ID, markAt); err != nil {
		t.Fatal(err)
	}
	if _, err := b.engine.MarkDismissed(b.repCtx, run.Items[1].ID, markAt); err != nil {
		t.Fatal(err)
	}

	// Next run: both marked deals are out, nothing else clears the bar —
	// the queue is honestly empty.
	nextClock := briefClock.Add(24 * time.Hour)
	next, err := b.engine.Rank(b.repCtx, nextClock)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Queue) != 0 || next.CandidateCount != 0 {
		t.Fatalf("post-mark queue = %v (candidates %d), want empty", queueDeals(next.Queue), next.CandidateCount)
	}

	// The marks bind for OTHER runs of the same user too, not just the
	// run they were made in — and for another user not at all: rep2's
	// own brief still ranks both deals (owned by rep1, visible under
	// row_scope=all).
	rep2 := b.As(b.Rep2, []ids.UUID{b.Team1}, integration.AdminPerms)
	rep2Ranking, err := b.engine.Rank(rep2, nextClock)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep2Ranking.Queue) != 2 {
		t.Fatalf("rep2's queue = %v, want both deals (marks are per-rep)", queueDeals(rep2Ranking.Queue))
	}

	// A dismissed deal reappears only when it materially changed: a new
	// linked activity after the mark makes Deal B re-eligible; Deal A
	// (acted, unchanged) stays out.
	fresh := integration.SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'email', 'they came back', '2026-06-04T10:00:00Z', 'manual', 'human:x')`, b.WS)
	integration.LinkActivity(t, owner, b.WS, fresh, "deal", b.dealB)

	reranked, err := b.engine.Rank(b.repCtx, nextClock)
	if err != nil {
		t.Fatal(err)
	}
	if len(reranked.Queue) != 1 || reranked.Queue[0].DealID != b.dealB {
		t.Fatalf("post-change queue = %v, want exactly the changed Deal B", queueDeals(reranked.Queue))
	}
	// The change since the last brief view is the momentum evidence.
	if reranked.Queue[0].Features.Momentum != briefMomentumChanged {
		t.Fatalf("re-proposed deal momentum = %v, want %v (it changed overnight)", reranked.Queue[0].Features.Momentum, briefMomentumChanged)
	}
}

// A rep's brief only ranks deals they can see: under team row scope a
// foreign team's deal never enters the candidate set, while an
// all-scope principal ranks it.
func TestBriefRankIsRowScoped(t *testing.T) {
	b := setupBrief(t)
	owner := integration.OwnerConn(t)

	foreign := b.seedBriefDeal(t, owner, "Foreign", b.stageA, int64Ptr(90_000_00), closeOn(briefClock, 3), &b.Rep3)

	scoped, err := b.engine.Rank(b.As(b.Rep1, []ids.UUID{b.Team1}, integration.RepPerms), briefClock)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range scoped.Queue {
		if item.DealID == foreign {
			t.Fatal("a team-scoped rep's brief ranked another team's deal — the candidate query leaks row scope")
		}
	}
	if len(scoped.Queue) != 2 {
		t.Fatalf("scoped queue = %v, want rep1's own two candidates", queueDeals(scoped.Queue))
	}

	all, err := b.engine.Rank(b.repCtx, briefClock)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range all.Queue {
		found = found || item.DealID == foreign
	}
	if !found {
		t.Fatalf("all-scope queue = %v misses the foreign deal", queueDeals(all.Queue))
	}
}
