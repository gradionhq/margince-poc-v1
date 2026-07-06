// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The PERF-3 / PERF-7 benchmark harness (B-EP05.21): seeds a §6.7
// volume tier, runs the canonical queries, records p50/p95/p99, gates
// red on a budget breach, and emits the ADR-0021 graph-store trigger
// evidence. The integration lane runs the SMB tier as a standing
// canary; `make bench-perf` runs the mid-market tier the PERF-7 SLO
// actually binds at.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
	"github.com/gradionhq/margince/backend/migrations"
)

// benchTierSpec sizes one §6.7 volume tier. Mid-market is the
// 250k–1M-contact band; the SLO binds at its floor.
type benchTierSpec struct {
	tier            search.BenchTier
	persons         int
	organizations   int
	bulkActivities  int // background timeline volume, linked cyclically to persons
	anchorTouches   int // activities on the measured graph anchor (the hot 360)
	relationships   int
	warmups, sample int
}

var benchTiers = map[search.BenchTier]benchTierSpec{
	search.BenchTierSMB: {
		tier: search.BenchTierSMB, persons: 10_000, organizations: 1_000,
		bulkActivities: 20_000, anchorTouches: 200, relationships: 5_000,
		warmups: 3, sample: 20,
	},
	search.BenchTierMidMarket: {
		tier: search.BenchTierMidMarket, persons: 250_000, organizations: 10_000,
		bulkActivities: 500_000, anchorTouches: 500, relationships: 50_000,
		warmups: 3, sample: 20,
	},
}

func TestPerfBudgetsHoldOnSeededVolumeTier(t *testing.T) {
	spec, ok := benchTiers[search.BenchTier(envOr("MARGINCE_BENCH_TIER", string(search.BenchTierSMB)))]
	if !ok {
		t.Fatalf("MARGINCE_BENCH_TIER must be one of smb, mid_market")
	}

	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatal(err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatal(err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatal(err)
	}

	ws := ids.NewV7()
	anchor := seedBenchTier(t, owner, ws, spec)

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := search.NewStore(pool)
	retriever := search.NewRetriever(store, nil)

	actx := benchAdminCtx(ws)

	report := search.BenchReport{Tier: spec.tier}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM relationship`).Scan(&report.RelationshipEdges); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM activity_link`).Scan(&report.ActivityLinkEdges); err != nil {
		t.Fatal(err)
	}

	// Canonical query 1 (PERF-3): ranked cross-object full-text search.
	ftsStats, err := benchRuns("search_fts", search.Perf3Budget, spec, func() error {
		page, err := store.Search(actx, search.Input{Query: "hamburg"})
		if err != nil {
			return err
		}
		if len(page.Hits) == 0 {
			return fmt.Errorf("fts benchmark query matched nothing — the fixture is wrong")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Canonical query 2 (PERF-7): the fixed-depth context-graph
	// assembly over the anchor's hot 360.
	graphStats, err := benchRuns(search.GraphQueryName, search.Perf7Budget, spec, func() error {
		assembled, err := retriever.AssembleContext(actx,
			datasource.EntityRef{Type: datasource.EntityPerson, ID: anchor},
			retrieval.AssembleOptions{MaxItems: 5})
		if err != nil {
			return err
		}
		if len(assembled.Sections) < 2 {
			return fmt.Errorf("graph assembly returned %d sections — the fixture is wrong", len(assembled.Sections))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	report.Queries = []search.QueryStats{ftsStats, graphStats}
	for _, q := range report.Queries {
		t.Logf("perfbench [%s]: %s p50=%s p95=%s p99=%s (budget %s, %d samples)",
			report.Tier, q.Query, q.P50, q.P95, q.P99, q.Budget, q.Samples)
	}

	// The ADR-0021 trigger evidence is computed and reported on every
	// run — a passing run is the "substrate confirmed" record.
	evidence := report.TriggerEvidence()
	t.Log(evidence.String())
	if evidence.GraphAssemblyP95 <= 0 {
		t.Fatal("trigger evidence must carry the measured graph-assembly p95")
	}
	if evidence.Tier != spec.tier {
		t.Fatalf("trigger evidence names tier %s, ran %s", evidence.Tier, spec.tier)
	}

	if err := report.Gate(); err != nil {
		t.Fatalf("PERF budget gate is red: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func benchRuns(name string, budget time.Duration, spec benchTierSpec, run func() error) (search.QueryStats, error) {
	for i := 0; i < spec.warmups; i++ {
		if err := run(); err != nil {
			return search.QueryStats{}, fmt.Errorf("%s warmup: %w", name, err)
		}
	}
	durations := make([]time.Duration, 0, spec.sample)
	for i := 0; i < spec.sample; i++ {
		start := time.Now()
		if err := run(); err != nil {
			return search.QueryStats{}, fmt.Errorf("%s run %d: %w", name, i, err)
		}
		durations = append(durations, time.Since(start))
	}
	return search.MeasureQuery(name, budget, durations)
}

func benchAdminCtx(ws ids.UUID) context.Context {
	grants := map[string]principal.ObjectGrant{}
	for _, object := range []string{"person", "organization", "deal", "lead", "activity"} {
		grants[object] = principal.ObjectGrant{Read: true}
	}
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String(), UserID: ids.NewV7(),
		Permissions: principal.Permissions{Objects: grants, RowScope: principal.RowScopeAll},
	})
}

// seedBenchTier bulk-loads one volume tier through the owner
// connection (set-based inserts — the write shape has its own suites)
// and returns the graph-anchor person the PERF-7 query measures.
func seedBenchTier(t *testing.T, owner *pgx.Conn, ws ids.UUID, spec benchTierSpec) ids.UUID {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seeding %s tier: %v", spec.tier, err)
		}
	}

	exec(`INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Bench', 'bench', 'EUR')`, ws)

	// Every ~97th person carries the FTS token the canonical search
	// query hits, so the query does real ranking work over a real
	// selectivity, not a table scan of universal matches.
	exec(`INSERT INTO person (workspace_id, full_name, source, captured_by)
	      SELECT $1, 'Person ' || i || CASE WHEN i % 97 = 0 THEN ' Hamburg' ELSE '' END, 'manual', 'human:bench'
	      FROM generate_series(1, $2) AS i`, ws, spec.persons)
	exec(`INSERT INTO organization (workspace_id, display_name, source, captured_by)
	      SELECT $1, 'Org ' || i || CASE WHEN i % 89 = 0 THEN ' Hamburg GmbH' ELSE '' END, 'manual', 'human:bench'
	      FROM generate_series(1, $2) AS i`, ws, spec.organizations)

	// Background timeline volume: activities linked cyclically across
	// the person population — the activity_link fan the recursive walk
	// competes with.
	// The cyclic assignment precomputes each row's target ordinal so the
	// join is a plain hashable equijoin — an expression joining both
	// sides' row_numbers forces the planner into a nested loop that is
	// pathological at the mid-market tier.
	exec(`WITH act AS (
	        INSERT INTO activity (workspace_id, kind, subject, body, occurred_at, source, captured_by)
	        SELECT $1,
	               CASE WHEN i % 5 = 0 THEN 'task' ELSE 'email' END,
	               'Subject ' || i || CASE WHEN i % 101 = 0 THEN ' Hamburg' ELSE '' END,
	               'Body ' || i,
	               now() - (i % 720 || ' hours')::interval,
	               'manual', 'human:bench'
	        FROM generate_series(1, $2) AS i
	        RETURNING id
	      ), total AS (
	        SELECT count(*) AS n FROM person WHERE workspace_id = $1
	      ), numbered AS (
	        SELECT id, (row_number() OVER () - 1) % (SELECT n FROM total) + 1 AS target_rn FROM act
	      ), people AS (
	        SELECT id, row_number() OVER () AS rn FROM person WHERE workspace_id = $1
	      )
	      INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
	      SELECT $1, n.id, 'person', p.id
	      FROM numbered n JOIN people p ON p.rn = n.target_rn`, ws, spec.bulkActivities)

	// Employment edges for the ADR-0021 edge-count evidence.
	exec(`WITH total AS (
	        SELECT count(*) AS n FROM organization WHERE workspace_id = $1
	      ), people AS (
	        SELECT id, (row_number() OVER () - 1) % (SELECT n FROM total) + 1 AS target_rn
	        FROM person WHERE workspace_id = $1 LIMIT $2
	      ), orgs AS (
	        SELECT id, row_number() OVER () AS rn FROM organization WHERE workspace_id = $1
	      )
	      INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
	      SELECT $1, 'employment', p.id, o.id, 'manual', 'human:bench'
	      FROM people p JOIN orgs o ON o.rn = p.target_rn`, ws, spec.relationships)

	// The measured anchor: one person with a hot 360 — touches linked
	// to it AND to organizations, so hop 2 has real expansion work.
	var anchor ids.UUID
	if err := owner.QueryRow(ctx,
		`INSERT INTO person (workspace_id, full_name, source, captured_by)
		 VALUES ($1, 'Anchor Hamburg', 'manual', 'human:bench') RETURNING id`, ws).Scan(&anchor); err != nil {
		t.Fatalf("seeding anchor: %v", err)
	}
	exec(`WITH act AS (
	        INSERT INTO activity (workspace_id, kind, subject, body, occurred_at, source, captured_by)
	        SELECT $1,
	               CASE WHEN i % 4 = 0 THEN 'task' ELSE 'meeting' END,
	               'Anchor touch ' || i, 'Anchor body ' || i,
	               now() - (i || ' hours')::interval,
	               'manual', 'human:bench'
	        FROM generate_series(1, $3) AS i
	        RETURNING id
	      ), total AS (
	        SELECT count(*) AS n FROM organization WHERE workspace_id = $1
	      ), numbered AS (
	        SELECT id, (row_number() OVER () - 1) % (SELECT n FROM total) + 1 AS target_rn FROM act
	      ), links AS (
	        INSERT INTO activity_link (workspace_id, activity_id, entity_type, person_id)
	        SELECT $1, id, 'person', $2 FROM numbered
	        RETURNING activity_id
	      ), orgs AS (
	        SELECT id, row_number() OVER () AS rn FROM organization WHERE workspace_id = $1
	      )
	      INSERT INTO activity_link (workspace_id, activity_id, entity_type, organization_id)
	      SELECT $1, n.id, 'organization', o.id
	      FROM numbered n JOIN orgs o ON o.rn = n.target_rn`, ws, anchor, spec.anchorTouches)

	exec(`ANALYZE person, organization, activity, activity_link, relationship`)
	return anchor
}
