// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// Deal health (B-E09.15/.16/.17, formulas-and-rules §10.5) over real
// rows: fixed seed + fixed clock reproduces the spec's worked example;
// the score is advisory — computing it mutates nothing; a §8-stalled
// deal reads at-risk with its commitments factor zeroed.

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// healthClock is the fixed evaluation instant every seeded timestamp is
// pinned relative to.
var healthClock = time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

// seedHealthyDeal builds the §10.5 worked-example fixture: last
// activity 5 days ago (recency 0.8), 16.8 days in stage against the
// 14-day fallback (1.2× → velocity 0.6), two two-way-engaged
// stakeholders plus a one-way broadcast target (engagement 2/3), one
// open task due in the future (commitments 1.0, not stalled).
func seedHealthyDeal(t *testing.T, e *authzEnv, owner *pgx.Conn) (deal ids.UUID, engaged []ids.UUID, freshest ids.UUID) {
	t.Helper()
	ctx := context.Background()
	pipeline, open, _ := dealFixture(t, e)

	d, err := e.deals.CreateDeal(e.admin(), deals.CreateDealInput{
		Name: "Worked Example", PipelineID: pipeline, StageID: open, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	deal = ids.UUID(d.Id)
	if _, err := owner.Exec(ctx,
		`UPDATE deal SET last_activity_at = $2 WHERE id = $1`,
		deal, healthClock.AddDate(0, 0, -5)); err != nil {
		t.Fatal(err)
	}
	// CreateDeal wrote the initial stage-entry history row at wall time;
	// pin it so days-in-stage is exactly 1.2× the 14-day fallback.
	if _, err := owner.Exec(ctx,
		`UPDATE deal_stage_history SET changed_at = $2 WHERE deal_id = $1`,
		deal, healthClock.Add(-time.Duration(1.2*14*24*float64(time.Hour)))); err != nil {
		t.Fatal(err)
	}

	// The recency evidence record: the freshest live activity on the deal.
	freshest = seedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, source, captured_by)
		VALUES ($1, $2, 'call', 'checkpoint', '2026-05-30T12:00:00Z', 'manual', 'human:x')`, e.ws)
	linkActivity(t, owner, e.ws, freshest, "deal", deal)

	// Two stakeholders with BOTH directions inside the 90-day window →
	// engaged; a third who only ever received our outbound → not.
	for i := 0; i < 2; i++ {
		engaged = append(engaged, seedStakeholder(t, e, owner, deal, "inbound", "outbound"))
	}
	seedStakeholder(t, e, owner, deal, "outbound", "outbound")

	// An open task due AFTER the clock: a commitment, but not overdue.
	// Logged BEFORE the checkpoint call so the call stays the deal's
	// freshest activity — the recency evidence the test pins.
	pending := seedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, due_at, source, captured_by)
		VALUES ($1, $2, 'task', 'send proposal', '2026-05-25T12:00:00Z', '2026-06-08T12:00:00Z', 'manual', 'human:x')`, e.ws)
	linkActivity(t, owner, e.ws, pending, "deal", deal)
	return deal, engaged, freshest
}

// seedStakeholder creates a person, ties them to the deal as a
// deal_stakeholder, and gives them one email in each named direction
// three days before the clock.
func seedStakeholder(t *testing.T, e *authzEnv, owner *pgx.Conn, deal ids.UUID, directions ...string) ids.UUID {
	t.Helper()
	person := seedRow(t, owner, `INSERT INTO person (id, workspace_id, full_name, source, captured_by)
		VALUES ($1, $2, 'Stakeholder', 'manual', 'human:x')`, e.ws)
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO relationship (workspace_id, kind, person_id, deal_id, source, captured_by)
		 VALUES ($1, 'deal_stakeholder', $2, $3, 'manual', 'human:x')`, e.ws, person, deal); err != nil {
		t.Fatal(err)
	}
	for _, direction := range directions {
		touch := seedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
			VALUES ($1, $2, 'email', 'touch', '2026-06-01T12:00:00Z', '`+direction+`', 'manual', 'human:x')`, e.ws)
		linkActivity(t, owner, e.ws, touch, "person", person)
	}
	return person
}

// linkActivity attaches an activity to a person or deal through the
// polymorphic link table.
func linkActivity(t *testing.T, owner *pgx.Conn, ws, activity ids.UUID, entityType string, entity ids.UUID) {
	t.Helper()
	column := "deal_id"
	if entityType == "person" {
		column = "person_id"
	}
	if _, err := owner.Exec(context.Background(),
		`INSERT INTO activity_link (workspace_id, activity_id, entity_type, `+column+`) VALUES ($1, $2, $3, $4)`,
		ws, activity, entityType, entity); err != nil {
		t.Fatal(err)
	}
}

func TestDealHealthReproducesTheWorkedExampleOverSeededRows(t *testing.T) {
	e := setupAuthz(t)
	owner := ownerConn(t)
	deal, engaged, freshest := seedHealthyDeal(t, e, owner)
	ctx := e.as(e.rep1, []ids.UUID{e.team1}, adminPerms)

	got, err := e.deals.DealHealth(ctx, deal, healthClock)
	if err != nil {
		t.Fatal(err)
	}
	want := deals.DealHealthFactors{ActivityRecency: 0.8, StageVelocity: 0.6, Engagement: 2.0 / 3.0, Commitments: 1.0}
	if got.Factors != want {
		t.Fatalf("factors = %+v, want %+v", got.Factors, want)
	}
	wantHealth := 0.30*0.8 + 0.25*0.6 + 0.20*(2.0/3.0) + 0.25*1.0
	if math.Abs(got.Health-wantHealth) > 1e-9 {
		t.Fatalf("health = %.12f, want %.12f", got.Health, wantHealth)
	}
	if got.AtRisk {
		t.Fatalf("worked example is healthy, not at risk (%.3f)", got.Health)
	}

	// The evidence names the exact source records (B-E09.16).
	if got.Evidence.MostRecentActivityID == nil || *got.Evidence.MostRecentActivityID != freshest {
		t.Fatalf("recency evidence = %v, want the freshest deal activity %s", got.Evidence.MostRecentActivityID, freshest)
	}
	if len(got.Evidence.EngagedStakeholderIDs) != 2 {
		t.Fatalf("engaged stakeholders = %v, want exactly the two two-way persons", got.Evidence.EngagedStakeholderIDs)
	}
	seen := map[ids.UUID]bool{}
	for _, id := range got.Evidence.EngagedStakeholderIDs {
		seen[id] = true
	}
	for _, id := range engaged {
		if !seen[id] {
			t.Fatalf("engaged evidence %v misses two-way stakeholder %s", got.Evidence.EngagedStakeholderIDs, id)
		}
	}
	if len(got.Evidence.OverdueTaskIDs) != 0 {
		t.Fatalf("a task due in the future is not overdue: %v", got.Evidence.OverdueTaskIDs)
	}
	if got.Evidence.ExpectedDaysInStage != 14 {
		t.Fatalf("expected days = %f, want the 14-day fallback (no won-deal history)", got.Evidence.ExpectedDaysInStage)
	}

	// Determinism: the same seed + clock reproduces the same score.
	again, err := e.deals.DealHealth(ctx, deal, healthClock)
	if err != nil {
		t.Fatal(err)
	}
	if again.Health != got.Health || again.Factors != got.Factors {
		t.Fatalf("same seed + clock → %.12f then %.12f", got.Health, again.Health)
	}
}

// The B-E09.17 advisory guard: computing health writes NOTHING — the
// deal row (stage, status, version, every column) is byte-identical
// after the computation.
func TestDealHealthComputationNeverMutatesTheDeal(t *testing.T) {
	e := setupAuthz(t)
	owner := ownerConn(t)
	deal, _, _ := seedHealthyDeal(t, e, owner)
	ctx := e.as(e.rep1, []ids.UUID{e.team1}, adminPerms)

	snapshot := func() string {
		var row string
		if err := owner.QueryRow(context.Background(),
			`SELECT to_jsonb(d)::text FROM deal d WHERE id = $1`, deal).Scan(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	before := snapshot()
	if _, err := e.deals.DealHealth(ctx, deal, healthClock); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("computing health mutated the deal row:\nbefore: %s\nafter:  %s", before, after)
	}
}

// A §8-stalled deal reads at-risk: recency and velocity floored by the
// idle time, commitments zeroed by the stalled flag even though the
// overdue task still shows in the evidence.
func TestStalledDealReadsAtRisk(t *testing.T) {
	e := setupAuthz(t)
	owner := ownerConn(t)
	pipeline, open, _ := dealFixture(t, e)
	ctx := e.as(e.rep1, []ids.UUID{e.team1}, adminPerms)

	d, err := e.deals.CreateDeal(e.admin(), deals.CreateDealInput{
		Name: "Gone Quiet", PipelineID: pipeline, StageID: open, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	deal := ids.UUID(d.Id)
	idleSince := healthClock.AddDate(0, 0, -90)
	if _, err := owner.Exec(context.Background(),
		`UPDATE deal SET last_activity_at = $2 WHERE id = $1`, deal, idleSince); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(context.Background(),
		`UPDATE deal_stage_history SET changed_at = $2 WHERE deal_id = $1`, deal, idleSince); err != nil {
		t.Fatal(err)
	}
	overdue := seedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, due_at, source, captured_by)
		VALUES ($1, $2, 'task', 'never followed up', '2026-03-06T12:00:00Z', '2026-03-20T12:00:00Z', 'manual', 'human:x')`, e.ws)
	linkActivity(t, owner, e.ws, overdue, "deal", deal)

	got, err := e.deals.DealHealth(ctx, deal, healthClock)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Evidence.Stalled {
		t.Fatal("90 idle days must read as stalled (§8)")
	}
	if got.Factors.Commitments != 0.0 {
		t.Fatalf("stalled deal commitments = %f, want 0.0 regardless of overdue count", got.Factors.Commitments)
	}
	if len(got.Evidence.OverdueTaskIDs) != 1 || got.Evidence.OverdueTaskIDs[0] != overdue {
		t.Fatalf("overdue evidence = %v, want [%s]", got.Evidence.OverdueTaskIDs, overdue)
	}
	if !got.AtRisk || got.Health >= 0.35 {
		t.Fatalf("stalled+silent deal → health %.3f (at_risk=%v), want < 0.35 and at_risk", got.Health, got.AtRisk)
	}
}
