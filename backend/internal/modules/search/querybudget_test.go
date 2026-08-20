// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Postgres raises ONE SQLSTATE for every reason a statement stops early, so the
// spent-budget answer and the vanished-caller error are told apart by the
// context and nothing else. Getting this backwards reports a caller's own
// timeout as this installation's plan being too slow — and answers a degraded
// page to somebody who is no longer there to read it.
func TestASpentBudgetIsToldApartFromACallerWhoLeft(t *testing.T) {
	t.Parallel()
	canceled := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}

	if !budgetSpent(context.Background(), canceled) {
		t.Error("a cancelled statement under a live request was not read as the budget being spent")
	}

	gone, abandon := context.WithCancel(context.Background())
	abandon()
	if budgetSpent(gone, canceled) {
		t.Error("a caller who left was reported as this plan exceeding its budget")
	}

	unrelated := &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
	if budgetSpent(context.Background(), unrelated) {
		t.Error("an unrelated SQLSTATE was read as the budget being spent")
	}
}

// An abandoned statement returns no rows, and the one thing it must never do is
// claim they are all of them: complete_exact is the only verdict a caller may
// count with, and an empty page carrying it says the workspace holds nothing.
func TestAnAbandonedAnswerIsNeverReportedAsComplete(t *testing.T) {
	t.Parallel()
	exact := validatedPlanDoc(readerFor(entityDeal), t, `{"version": "v1", "target": "deal"}`)
	if got := coverageOf(exact, answerShape{abandoned: true}); got != CoveragePartialDegraded {
		t.Errorf("an abandoned exact plan answered coverage %q", got)
	}
	ranked := validatedPlanDoc(readerFor(entityDeal), t, `{"version": "v1", "target": "deal", "similar_to": "roofing"}`)
	if got := coverageOf(ranked, answerShape{abandoned: true}); got != CoveragePartialDegraded {
		t.Errorf("an abandoned ranked plan answered coverage %q", got)
	}
	// The verdict is still reachable when nothing went wrong, which is what
	// makes the two above worth asserting.
	if got := coverageOf(exact, answerShape{}); got != CoverageCompleteExact {
		t.Errorf("a whole exact answer reported coverage %q", got)
	}
}

// The executor bounds a COPY of the store it is handed, never the caller's.
//
// That is what keeps the contract /search endpoint out of this: it shares the
// same store type, and a constructor that bounded the handle in place would
// have given every unrelated caller of that store this executor's ceiling.
func TestTheExecutorBoundsItsOwnStoreAndNotTheCallersOne(t *testing.T) {
	t.Parallel()
	handle := database.Bind(nil, func(context.Context) (ids.WorkspaceID, error) { return ids.WorkspaceID{}, nil })
	store := NewStore(handle)

	e := NewQueryExecutorWithBudget(store, nil, nil, time.Millisecond)

	if e.budget != time.Millisecond {
		t.Errorf("the executor's budget is %s", e.budget)
	}
	if e.store == store {
		t.Error("the executor kept the caller's store, so bounding it bounded theirs too")
	}
	if e.store.db == store.db {
		t.Error("the executor shares the caller's handle, so the ceiling reaches every other user of it")
	}
	// And the production constructor arms one at all — an executor built the
	// way compose builds it must not run unbounded.
	if NewQueryExecutor(store, nil, nil).budget != planStatementBudget {
		t.Error("the composed executor carries no budget")
	}
}
