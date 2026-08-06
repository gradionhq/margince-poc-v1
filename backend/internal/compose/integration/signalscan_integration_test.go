// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The first thing in this product that ever writes a signal (SIG-F-3).
//
// The table has existed since migration 0047 with a card above it, and the only
// writer was the human-only POST /signals — so every account answered "no
// signal", however loudly its own correspondence said otherwise.

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func ghostedScan(t *testing.T, e *Env, now time.Time) int {
	t.Helper()
	return ghostedPass(t, e, now).Raised
}

// ghostedPass runs the deterministic producer and reports everything it did,
// for the tests that care whether an account was CONSIDERED as well as whether
// a signal was written.
func ghostedPass(t *testing.T, e *Env, now time.Time) compose.GhostedPass {
	t.Helper()
	var pass compose.GhostedPass
	// The SAME context the transaction was opened with: the write shape stamps
	// captured_by from the bound actor, so a bare context writes no signal at
	// all — which is what a producer running without a principal deserves.
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		var err error
		pass, err = compose.WriteGhostedSignals(ctx, tx,
			ids.From[ids.WorkspaceKind](e.WS), now)
		return err
	}); err != nil {
		t.Fatalf("ghosted scan: %v", err)
	}
	return pass
}

// mailViaEmployee logs one interaction with a contact who works at the account
// — the shape capture writes, where the message names a PERSON and the account
// is reached only through their employment. Each call is a different contact,
// so two calls are two colleagues at the same account.
func mailViaEmployee(t *testing.T, e *Env, org ids.UUID, subject, direction string, at time.Time) {
	t.Helper()
	owner := OwnerConn(t)
	id := accountMailDirectedAt(t, owner, e.WS, subject, direction, at)
	LinkActivity(t, owner, e.WS, id, "person", employeeOf(t, e, org, subject+" contact"))
}

func openSignalKinds(t *testing.T, e *Env, org ids.UUID) []string {
	t.Helper()
	var kinds []string
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT kind FROM signal WHERE resolved_org_id = $1 AND status = 'open'`, org)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind string
			if err := rows.Scan(&kind); err != nil {
				return err
			}
			kinds = append(kinds, kind)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading signals: %v", err)
	}
	return kinds
}

func TestGhostedThreadIsRaisedOnceAndSurvivesARepeatPass(t *testing.T) {
	e := Setup(t)
	now := time.Now().UTC()

	org := e.SeedOrg(t, "Silent Co", &e.Rep1)
	// An account worth chasing: without this the rule stays quiet, because an
	// unanswered fortnight on an account nobody works is not an observation
	// about a relationship.
	e.WsExec(t, `UPDATE organization SET lifecycle = 'opportunity' WHERE id = $1`, org)
	mailViaEmployee(t, e, org, "Update zu Margince", "outbound", now.AddDate(0, 0, -20))

	if written := ghostedScan(t, e, now); written != 1 {
		t.Fatalf("first pass wrote %d signals, want 1", written)
	}
	if kinds := openSignalKinds(t, e, org); len(kinds) != 1 || kinds[0] != "ghosted_thread" {
		t.Fatalf("open signals = %v, want one ghosted_thread", kinds)
	}

	// The producer runs hourly. An unchanged account must raise nothing.
	if written := ghostedScan(t, e, now.Add(time.Hour)); written != 0 {
		t.Errorf("a repeat pass wrote %d signals, want none", written)
	}
	if kinds := openSignalKinds(t, e, org); len(kinds) != 1 {
		t.Errorf("open signals after the repeat pass = %v, want the original one", kinds)
	}
}

func TestADismissedGhostedSignalDoesNotComeBack(t *testing.T) {
	e := Setup(t)
	now := time.Now().UTC()

	org := e.SeedOrg(t, "Dismissed Co", &e.Rep1)
	e.WsExec(t, `UPDATE organization SET lifecycle = 'customer' WHERE id = $1`, org)
	mailViaEmployee(t, e, org, "Following up", "outbound", now.AddDate(0, 0, -30))
	ghostedScan(t, e, now)

	e.WsExec(t, `UPDATE signal SET status = 'dismissed' WHERE resolved_org_id = $1`, org)

	// The fingerprint index covers dismissed rows, so the same silence cannot
	// raise again — an index that freed the key on dismissal would be the
	// opposite of dismissing it.
	if written := ghostedScan(t, e, now.Add(24*time.Hour)); written != 0 {
		t.Errorf("a dismissed signal came back: the pass wrote %d", written)
	}
}

func TestGhostedStaysQuietWhenTheyWroteLastOrNobodyIsWorkingTheAccount(t *testing.T) {
	e := Setup(t)
	now := time.Now().UTC()

	// They answered — and a COLLEAGUE of the person we wrote to answered, which
	// is the ordinary way a company replies. Resolving the account through a
	// direct link on the message could not see that at all: the reply named a
	// different person, so the account looked unanswered and the rule fired on
	// a relationship that was in fact alive.
	answered := e.SeedOrg(t, "They Replied", &e.Rep1)
	e.WsExec(t, `UPDATE organization SET lifecycle = 'opportunity' WHERE id = $1`, answered)
	mailViaEmployee(t, e, answered, "Proposal", "outbound", now.AddDate(0, 0, -30))
	mailViaEmployee(t, e, answered, "Re: Proposal", "inbound", now.AddDate(0, 0, -20))

	// Nobody is working this one: no open deal, and a lifecycle that is not live.
	idle := e.SeedOrg(t, "Nobody's Account", &e.Rep1)
	e.WsExec(t, `UPDATE organization SET lifecycle = 'disqualified' WHERE id = $1`, idle)
	mailViaEmployee(t, e, idle, "Last try", "outbound", now.AddDate(0, 0, -60))

	if written := ghostedScan(t, e, now); written != 0 {
		t.Errorf("the rule fired %d times on accounts it should ignore", written)
	}
}

// openSignalKindsAs reads the account's open signals as one named user,
// through the same scope clause every real reader composes.
//
// It grants that user ALL row scope and the signal read object grant on
// purpose: the only thing left that can withhold a row is the signal's own
// visibility, so a test using it cannot pass because some other gate happened
// to fire.
func openSignalKindsAs(t *testing.T, e *Env, user ids.UUID, org ids.UUID) []string {
	t.Helper()
	ctx := e.As(user, []ids.UUID{e.Team1}, AdminWithSignals)
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos := arg(org)
	clause, err := auth.SignalScopeClause(ctx, "s", arg)
	if err != nil {
		t.Fatalf("build the signal scope clause: %v", err)
	}
	if clause == "" {
		clause = "true"
	}
	var kinds []string
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, fmt.Sprintf(
			`SELECT s.kind FROM signal s
			  WHERE s.resolved_org_id = $%d AND s.status = 'open'
			    AND s.archived_at IS NULL AND %s`, orgPos, clause), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind string
			if err := rows.Scan(&kind); err != nil {
				return err
			}
			kinds = append(kinds, kind)
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("reading signals as %s: %v", user, err)
	}
	return kinds
}
