// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// What a plan is allowed to COST, and what it answers when it costs more.
//
// The page limit bounds the rows a plan RETURNS and not the rows it visits: a
// traversal compiles to a LATERAL join re-executed once per outer candidate
// row, and a predicate that matches almost nothing still makes the planner walk
// the table to prove it. So the ceiling is a time budget, spent by whichever of
// the plan's two transactions gets there first, and running out of it is an
// answer rather than a fault.

import (
	"context"
	"fmt"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// CodePlanExceededBudget is the note a plan carries when the statement ran out
// of time and was abandoned, so the answer has NO rows rather than a short page
// of them.
//
// A plan can cost far more than its page suggests — a traversal is a nested
// loop, and a predicate that matches almost nothing still walks the table to
// prove it — so a caller told only that the answer is partial would read the
// empty page as an empty workspace.
const CodePlanExceededBudget = "plan_exceeded_time_budget"

// planStatementBudget is how long one plan may hold a connection.
//
// It matches the filter preview's ceiling, which is the tree's only other one,
// and for the same reason: an agent still waiting after five seconds has
// already lost the turn it asked the question in. An indexed plan with a page
// limit answers an SMB-sized corpus in milliseconds, so this bounds the plans
// that cannot be answered at all rather than the ones that are merely large.
const planStatementBudget = 5 * time.Second

// NewQueryExecutorWithBudget is the same executor with an explicit ceiling, for
// the tests that have to prove the ceiling is really armed on real statements.
//
// It parameterizes the production executor rather than standing in for it: what
// a test chooses is the number, and the bounded handle, the classification and
// the answer it produces are the ones a caller gets. The budget is fixed at
// construction because compose builds ONE executor that every request shares —
// a setter on it would be a ceiling one request could change under another.
//
// A zero budget means unbounded, because a duration's zero value cannot be told
// apart from one nobody set. Nothing guards against passing it: NewQueryExecutor
// is the only caller that is not a test, and it passes the constant.
func NewQueryExecutorWithBudget(store *Store, embedder Embedder, columns ColumnReader,
	budget time.Duration,
) *QueryExecutor {
	// The budget is bound to the store's HANDLE, so it reaches every
	// transaction answering this plan opens — the ranking lane as well as the
	// exact one. Bounding the statement in answerRows alone would have left
	// a similar_to plan's ranking as unbounded as it was before, which is the
	// same defect one lane to the left.
	return &QueryExecutor{store: store.bounded(budget), embedder: embedder, columns: columns, budget: budget}
}

// abandoned is the answer a plan gets when it ran out of its budget, and it is
// the same answer whichever lane spent it.
//
// No rows, because a cancelled statement had found only whatever the planner
// happened to reach first — an arbitrary subset dressed as a page. The note is
// then the only thing between an empty answer and a wrong conclusion, so it
// says what to narrow rather than that something went wrong.
//
// Whatever notes the answer had already collected are kept: a ranking that
// degraded before the budget ran out still degraded, and the caller reading two
// notes learns both things that happened to their plan.
func (e *QueryExecutor) abandoned(plan ValidatedPlan, result QueryResult) QueryResult {
	result.Rows = nil
	result.Notes = append(result.Notes, QueryNote{
		Code: CodePlanExceededBudget,
		Detail: fmt.Sprintf("this plan did not finish within %s and was abandoned, so no rows are returned; "+
			"narrow it — predicate a field the records are indexed on, or ask without the traversal",
			e.budget),
	})
	result.Coverage = coverageOf(plan, answerShape{abandoned: true})
	return result
}

// budgetSpent tells this plan's own ceiling apart from every other reason a
// statement stops before answering.
//
// Postgres raises the same 57014 for a spent statement_timeout, an operator
// cancelling the backend, and the client going away, so the SQLSTATE alone
// cannot carry this judgement. The one that must not be misread is the caller:
// A CANCELLED REQUEST IS NOT A DEGRADED ANSWER — reporting their own vanished
// deadline as this plan being too slow describes their timeout as a property of
// the workspace, and there is nobody left to read the answer anyway. The live
// context separates that case, which is why the classification lives here, with
// the caller that holds one.
//
// An operator cancelling the backend under a live request reads as a spent
// budget, and is left that way: both mean the statement was stopped before it
// answered, and narrowing the plan is the caller's move either way.
func budgetSpent(ctx context.Context, err error) bool {
	return storekit.IsQueryCanceled(err) && ctx.Err() == nil
}
