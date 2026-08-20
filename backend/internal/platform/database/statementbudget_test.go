// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// recordingTx captures the statements a budget issues. The embedded interface
// is nil deliberately: a call to anything but Exec is a call this helper was
// never asked to stand in for, and panicking says so louder than a zero value.
type recordingTx struct {
	pgx.Tx
	issued []string
	// refuse is what Exec answers, for the caller that has to prove a database
	// refusing the ceiling does not read as a ceiling that was set.
	refuse error
}

func (r *recordingTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.issued = append(r.issued, fmt.Sprintf("%s %v", sql, args))
	return pgconn.CommandTag{}, r.refuse
}

// The budget reaches Postgres in the unit Postgres reads it in. statement_timeout
// with no unit is milliseconds, and a duration rendered as anything else would
// bound the statement to the wrong order of magnitude. It rides set_config
// rather than a built SET LOCAL, so the value is a bound parameter.
func TestAStatementBudgetIsBoundInMilliseconds(t *testing.T) {
	t.Parallel()
	tx := &recordingTx{}
	if err := BoundStatement(context.Background(), tx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if len(tx.issued) != 1 || tx.issued[0] != `SELECT set_config('statement_timeout', $1, true) [5000]` {
		t.Fatalf("the statements issued were %q", tx.issued)
	}
}

// A budget too short to express in milliseconds is the trap this rounds away
// from: Postgres reads statement_timeout = 0 as NO timeout, so truncating a
// sub-millisecond budget would unbound the statement the call was made to bound
// — the one outcome worse than never calling it.
func TestABudgetShorterThanAMillisecondStillBoundsTheStatement(t *testing.T) {
	t.Parallel()
	tx := &recordingTx{}
	if err := BoundStatement(context.Background(), tx, 500*time.Microsecond); err != nil {
		t.Fatal(err)
	}
	if len(tx.issued) != 1 || tx.issued[0] != `SELECT set_config('statement_timeout', $1, true) [1]` {
		t.Fatalf("the statements issued were %q", tx.issued)
	}
}

// Zero is not "no ceiling wanted", it is a caller who computed one wrong. It is
// refused rather than passed through, because passing it through is silently
// indistinguishable from the unbounded path.
func TestANonPositiveBudgetIsRefusedAndIssuesNothing(t *testing.T) {
	t.Parallel()
	for _, budget := range []time.Duration{0, -time.Second} {
		tx := &recordingTx{}
		err := BoundStatement(context.Background(), tx, budget)
		if err == nil {
			t.Fatalf("a budget of %s was accepted", budget)
		}
		if len(tx.issued) != 0 {
			t.Errorf("a refused budget of %s still issued %q", budget, tx.issued)
		}
	}
}

// A bounded handle re-bound to another tenant keeps its ceiling. The fleet
// passes that re-bind run the same statements against the same tables, and a
// budget that fell off at the re-bind would be one nobody could see was
// missing.
func TestABoundedHandleKeepsItsCeilingWhenReboundToAnotherTenant(t *testing.T) {
	t.Parallel()
	bounded := Bind(nil, func(context.Context) (ids.WorkspaceID, error) { return ids.WorkspaceID{}, nil }).
		Bounded(5 * time.Second)

	if got := bounded.ForWorkspace(ids.WorkspaceID{}).budget; got != 5*time.Second {
		t.Errorf("the re-bound handle's budget is %s", got)
	}
	// An unbounded handle stays unbounded, so the ceiling is something a caller
	// asks for rather than something re-binding hands out.
	plain := Bind(nil, func(context.Context) (ids.WorkspaceID, error) { return ids.WorkspaceID{}, nil })
	if got := plain.ForWorkspace(ids.WorkspaceID{}).budget; got != 0 {
		t.Errorf("an unbounded handle grew a budget of %s", got)
	}
}

// A database that refuses the ceiling must not leave the caller believing there
// is one. The error says which budget failed, because "bounding the statement"
// with no number leaves an operator reading it no way to tell a misconfigured
// ceiling from an unreachable database.
func TestAStatementThatCannotBeBoundFailsRatherThanRunningUnbounded(t *testing.T) {
	t.Parallel()
	refused := errors.New("connection reset")
	tx := &recordingTx{refuse: refused}

	err := BoundStatement(context.Background(), tx, 5*time.Second)

	if !errors.Is(err, refused) {
		t.Fatalf("the refusal did not reach the caller: %v", err)
	}
	if !strings.Contains(err.Error(), "5s") {
		t.Errorf("the error does not name the budget it failed to set: %v", err)
	}
}

// Bounding an un-injected handle must not panic where it is WIRED. A store
// built without a database is how this tree asserts a gate that answers before
// any query runs, and those tests key on the sentinel below — so the bounded
// path owes them the same answer the unbounded one gives.
func TestBoundingAnUninjectedHandleStillAnswersTheSentinel(t *testing.T) {
	t.Parallel()
	var unwired *DB

	bounded := unwired.Bounded(5 * time.Second)

	err := bounded.Tx(context.Background(), func(pgx.Tx) error {
		t.Fatal("a transaction body ran on a handle with no database")
		return nil
	})
	if !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("a bounded un-injected handle answered %v, not the no-workspace sentinel", err)
	}
}
