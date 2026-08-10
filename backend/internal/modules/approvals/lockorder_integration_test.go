// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package approvals

// The group pre-lock, against a real database.
//
// What it has to prove is a claim about a moment INSIDE a transaction — that by
// the time a batch stager touches its first member, it already holds the locks
// on all of them — and no unit test can see inside a transaction. The proof is
// a second connection: a row this transaction holds refuses NOWAIT, and a row
// it does not hold takes the lock immediately.
//
// It deliberately does NOT try to reproduce the deadlock. A deadlock needs two
// transactions to interleave at a specific instant, so a test that asserts one
// happens is flaky in one direction and a test that asserts one does not is
// flaky in the other. The mechanism is what makes the deadlock impossible, so
// the mechanism is what is asserted here; the ORDER the locks are taken in is
// held by TestEveryMultiRowApprovalLockTakesTheCanonicalOrder, over the SQL.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// lockNotAvailable is what PostgreSQL answers a FOR UPDATE NOWAIT that would
// have had to wait — the observable "somebody else holds this row".
const lockNotAvailable = "55P03"

func TestTheGroupPreLockHoldsEveryPendingMemberAtOnce(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()

	// Three members of one act, staged as separate transactions so their
	// created_at really differ — the batch stager's loop would reach the third
	// last, which is exactly the row a per-member lock leaves free the longest.
	members := []ids.ApprovalID{
		e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-one"),
		e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-two"),
		e.stageInto(ctx, t, bundle, org, kindSiteLead, "lead-three"),
	}
	// A proposal of a different kind against the same account, to show the
	// pre-lock takes the group it named and not the whole target: locking rows a
	// batch will never touch would block decisions for no reason.
	other := e.stageInto(ctx, t, bundle, org, kindDeepRead, "the company facts")

	held := make(chan struct{})
	released := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
			if err := e.svc.LockPendingGroupInTx(ctx, tx, org, kindSiteLead); err != nil {
				return err
			}
			close(held)
			<-released
			return nil
		})
	}()
	<-held

	for i, id := range members {
		if err := e.lockNoWait(t, id); !isLockNotAvailable(err) {
			t.Errorf("member %d was free to lock while the group pre-lock was held (err = %v) — the "+
				"batch would acquire it later, in payload order, and a bundle decision walking the "+
				"same rows in (created_at, id) is the other half of a deadlock", i+1, err)
		}
	}
	if err := e.lockNoWait(t, other); err != nil {
		t.Errorf("a %s proposal against the same account was locked too (err = %v) — the pre-lock "+
			"must take the group it named, not every row sharing a target", kindDeepRead, err)
	}

	close(released)
	if err := <-done; err != nil {
		t.Fatalf("the pre-locking transaction: %v", err)
	}
}

// An act that stages MORE THAN ONE KIND against a target takes them in one
// statement, not one per kind.
//
// Re-proposing rebundles what it joins, so a bundle ends up holding a company's
// facts and its published people with different ages, and a decision walks that
// as ONE interleaved (created_at, id) sequence. Two ordered runs, one per kind,
// are not one order: the decision can hold a lead the act is about to want while
// waiting for a facts row the act already holds. So the kinds are variadic, and
// this is the test that says naming both of them means both are held at once.
func TestAMultiKindActLocksEveryKindItStages(t *testing.T) {
	e := setupStaging(t)
	ctx := e.asHumanWith(decidesEverything())
	org := e.organization(t)
	bundle := ids.NewV7()

	lead := e.stageInto(ctx, t, bundle, org, kindSiteLead, "the published person")
	facts := e.stageInto(ctx, t, bundle, org, kindDeepRead, "the company facts")

	held, released := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- database.WithWorkspaceTx(ctx, e.pool, func(tx pgx.Tx) error {
			if err := e.svc.LockPendingGroupInTx(ctx, tx, org, kindDeepRead, kindSiteLead); err != nil {
				return err
			}
			close(held)
			<-released
			return nil
		})
	}()
	<-held

	for _, member := range []struct {
		kind string
		id   ids.ApprovalID
	}{{kindSiteLead, lead}, {kindDeepRead, facts}} {
		if err := e.lockNoWait(t, member.id); !isLockNotAvailable(err) {
			t.Errorf("the %s member was free to lock while the act's pre-lock was held (err = %v) — "+
				"an act that names a kind and does not hold it takes that row's lock later, in its "+
				"own order, which is the half of the deadlock naming both kinds exists to remove",
				member.kind, err)
		}
	}

	close(released)
	if err := <-done; err != nil {
		t.Fatalf("the pre-locking transaction: %v", err)
	}
}

// lockNoWait tries to take one row's lock from the competing connection without
// waiting, and answers what PostgreSQL said.
func (e *stagingEnv) lockNoWait(t *testing.T, id ids.ApprovalID) error {
	t.Helper()
	tx := e.competingTx(t)
	var got ids.ApprovalID
	err := tx.QueryRow(context.Background(),
		`SELECT id FROM approval WHERE id = $1 FOR UPDATE NOWAIT`, id).Scan(&got)
	//craft:ignore swallowed-errors the rollback ends a probe whose only result is the error above, which is returned
	_ = tx.Rollback(context.Background())
	return err
}

func isLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable
}
