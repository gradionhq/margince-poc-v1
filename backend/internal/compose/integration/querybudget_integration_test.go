// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What a query plan is allowed to COST (#1847).
//
// A plan's page limit bounds the rows it returns and not the rows it visits: a
// traversal compiles to a LATERAL join re-executed once per outer candidate
// row, so a hop that admits almost nobody makes the statement walk the whole
// outer table before it can fill a single page. The ceiling that bounds that is
// proven here rather than in a unit test, because a SET LOCAL only bounds
// anything on a real statement in a real transaction — and because the same
// corpus has to show the ceiling does NOT cut a legitimate plan short.

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// budgetVolumeRows is how many outer rows the plan below must visit before it
// can answer.
//
// Measured rather than guessed: this corpus answers the plan in ~50 ms, so the
// one-millisecond budget the first test arms is fifty times short of finishing
// and the production budget is a hundred times more than enough. Both tests
// rest on that one gap, so it is stated once.
const budgetVolumeRows = 20000

// budgetVolumePlan walks every seeded person and probes the employment edge for
// each of them: the hop's own predicate matches the one seeded company, while
// the EDGE reaches only one person. A hop that admitted everybody would let the
// outer scan stop at the page limit after a dozen rows and prove nothing about
// what a plan can cost.
const budgetVolumePlan = `{"version": "v1", "target": "person",
	"traverse": {"relation": "organizations",
	             "where": [{"field": "address.city", "op": "eq", "value": "Volumeburg"}]}}`

// rankedVolumePlan ranks every seeded person, so the ranking lane has the whole
// corpus to score before the exact lane sees a candidate.
const rankedVolumePlan = `{"version": "v1", "target": "person", "similar_to": "Volume"}`

// seedBudgetVolume builds the corpus both tests run the same plan over: one
// company in one city, many people, and exactly ONE employment edge between
// them. It answers the person that edge reaches, which is the whole right
// answer to the plan.
func (q *queryEnv) seedBudgetVolume(t *testing.T) ids.UUID {
	t.Helper()
	ctx := q.admin()
	org := q.SeedID(t, `INSERT INTO organization (id, display_name, address_city, source, captured_by)
		VALUES ($1, 'Volume GmbH', 'Volumeburg', 'manual', 'human:x')`)
	if _, err := q.Owner.Exec(ctx, `INSERT INTO person (full_name, source, captured_by)
		SELECT 'Volume Person ' || i, 'manual', 'human:x' FROM generate_series(1, $1) AS i`,
		budgetVolumeRows); err != nil {
		t.Fatalf("seeding %d people: %v", budgetVolumeRows, err)
	}
	var employee ids.UUID
	if err := q.Owner.QueryRow(ctx, `INSERT INTO relationship (kind, person_id, organization_id, source, captured_by)
		SELECT 'employment', id, $1, 'manual', 'human:x' FROM person ORDER BY id LIMIT 1
		RETURNING person_id`, org).Scan(&employee); err != nil {
		t.Fatalf("seeding the one employment edge: %v", err)
	}
	// Without fresh statistics the planner sizes these tables from whatever the
	// last analyze saw, which on a just-reset database is nothing — and the plan
	// it picks is what decides the cost under test.
	if _, err := q.Owner.Exec(ctx, `ANALYZE person, organization, relationship`); err != nil {
		t.Fatalf("analyzing the seeded volume: %v", err)
	}
	return employee
}

// A plan that cannot finish inside its budget is abandoned and says so, rather
// than holding a backend for as long as the statement takes. query_workspace is
// a read any passport may issue, so a handful of concurrent expensive plans is
// the whole connection pool.
//
// The verdict has to be partial_degraded and NOT complete_exact. Both come back
// with no rows, and the difference between them is the difference between
// "narrow your question" and "this workspace holds nobody".
func TestQueryPlanThatOutlastsItsBudgetIsAbandonedRatherThanServed(t *testing.T) {
	q := setupQuery(t)
	q.seedBudgetVolume(t)
	q.executor = search.NewQueryExecutorWithBudget(q.Store, nil, search.NewColumnCatalog(q.DB()), time.Millisecond)

	result := q.run(q.admin(), t, budgetVolumePlan)

	if len(result.Rows) != 0 {
		t.Fatalf("an abandoned statement answered %d rows, which are only whatever the planner reached first",
			len(result.Rows))
	}
	if result.Coverage != search.CoveragePartialDegraded {
		t.Fatalf("coverage is %q", result.Coverage)
	}
	if len(result.Notes) != 1 || result.Notes[0].Code != search.CodePlanExceededBudget {
		t.Fatalf("notes are %+v", result.Notes)
	}
	// The note is the only thing standing between an empty page and a wrong
	// conclusion, so it has to say what to do about it.
	if result.Notes[0].Detail == "" {
		t.Error("the note tells the caller nothing they can act on")
	}
}

// The SAME plan over the SAME corpus answers completely under the production
// budget. Without this the test above would pass just as well against a ceiling
// set so low that nothing is ever answered, which is the failure mode a timeout
// invites: a bound tight enough to be safe and useless.
func TestQueryPlanWithinItsBudgetAnswersCompletely(t *testing.T) {
	q := setupQuery(t)
	employee := q.seedBudgetVolume(t)

	result := q.run(q.admin(), t, budgetVolumePlan)

	if len(result.Rows) != 1 || result.Rows[0].ID != employee {
		t.Fatalf("the plan answered %d rows; the one employed person is the whole answer", len(result.Rows))
	}
	if result.Coverage != search.CoverageCompleteExact {
		t.Fatalf("coverage is %q, so a plan that ran whole is reporting that it did not", result.Coverage)
	}
	if len(result.Notes) != 0 {
		t.Errorf("a complete answer carries notes: %+v", result.Notes)
	}
}

// The RANKING lane is bounded too, which is a different transaction from the
// one above.
//
// A similar_to plan ranks before it filters, and that ranking opens its own
// transaction inside the store — so a ceiling armed only on the exact lane's
// statement would leave every ranked plan as unbounded as it was before anyone
// thought about the cost. This is the same defect one lane to the left, and it
// is reachable through the same 🟢 read.
func TestARankedQueryPlanIsBoundedInTheRankingLaneToo(t *testing.T) {
	q := setupQuery(t)
	q.seedBudgetVolume(t)
	q.executor = search.NewQueryExecutorWithBudget(q.Store, nil, search.NewColumnCatalog(q.DB()), time.Millisecond)

	result := q.run(q.admin(), t, rankedVolumePlan)

	if len(result.Rows) != 0 {
		t.Fatalf("an abandoned ranking answered %d rows", len(result.Rows))
	}
	if result.Coverage != search.CoveragePartialDegraded {
		t.Fatalf("coverage is %q", result.Coverage)
	}
	if len(result.Notes) != 1 || result.Notes[0].Code != search.CodePlanExceededBudget {
		t.Fatalf("notes are %+v", result.Notes)
	}

	// The control, on the same corpus: under the production budget this lane
	// ranks the whole corpus in ~115ms and answers a page. Without it the
	// assertions above would hold just as well against a ranking lane that is
	// broken rather than bounded.
	q.executor = search.NewQueryExecutor(q.Store, nil, search.NewColumnCatalog(q.DB()))
	answered := q.run(q.admin(), t, rankedVolumePlan)
	if len(answered.Rows) == 0 {
		t.Fatal("the same ranked plan answers nothing under the production budget")
	}
	// No embedder is wired here, so the one note it does carry is the lane
	// saying it ranked lexically — never the budget.
	for _, note := range answered.Notes {
		if note.Code == search.CodePlanExceededBudget {
			t.Errorf("a plan inside its budget reported exceeding it: %+v", note)
		}
	}
}
