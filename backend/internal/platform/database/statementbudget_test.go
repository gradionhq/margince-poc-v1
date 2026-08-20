// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package database

import (
	"context"
	"fmt"
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
}

func (r *recordingTx) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	r.issued = append(r.issued, fmt.Sprintf("%s %v", sql, args))
	return pgconn.CommandTag{}, nil
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
