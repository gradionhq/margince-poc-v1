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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func ghostedScan(t *testing.T, e *Env, now time.Time) int {
	t.Helper()
	var written int
	// The SAME context the transaction was opened with: the write shape stamps
	// captured_by from the bound actor, so a bare context writes no signal at
	// all — which is what a producer running without a principal deserves.
	ctx := e.Admin()
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		var err error
		written, err = compose.WriteGhostedSignals(ctx, tx,
			ids.From[ids.WorkspaceKind](e.WS), now)
		return err
	}); err != nil {
		t.Fatalf("ghosted scan: %v", err)
	}
	return written
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
	owner := OwnerConn(t)
	now := time.Now().UTC()

	org := e.SeedOrg(t, "Silent Co", &e.Rep1)
	// An account worth chasing: without this the rule stays quiet, because an
	// unanswered fortnight on an account nobody works is not an observation
	// about a relationship.
	e.WsExec(t, `UPDATE organization SET lifecycle = 'opportunity' WHERE id = $1`, org)
	outbound := accountMailDirectedAt(t, owner, e.WS, "Update zu Margince", "outbound",
		now.AddDate(0, 0, -20))
	linkToOrg(t, e, outbound, org)

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
	owner := OwnerConn(t)
	now := time.Now().UTC()

	org := e.SeedOrg(t, "Dismissed Co", &e.Rep1)
	e.WsExec(t, `UPDATE organization SET lifecycle = 'customer' WHERE id = $1`, org)
	outbound := accountMailDirectedAt(t, owner, e.WS, "Following up", "outbound", now.AddDate(0, 0, -30))
	linkToOrg(t, e, outbound, org)
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
	owner := OwnerConn(t)
	now := time.Now().UTC()

	// They answered: the thread is not ghosted, it is ours to read.
	answered := e.SeedOrg(t, "They Replied", &e.Rep1)
	e.WsExec(t, `UPDATE organization SET lifecycle = 'opportunity' WHERE id = $1`, answered)
	ours := accountMailDirectedAt(t, owner, e.WS, "Proposal", "outbound", now.AddDate(0, 0, -30))
	linkToOrg(t, e, ours, answered)
	theirs := accountMailDirectedAt(t, owner, e.WS, "Re: Proposal", "inbound", now.AddDate(0, 0, -20))
	linkToOrg(t, e, theirs, answered)

	// Nobody is working this one: no open deal, and a lifecycle that is not live.
	idle := e.SeedOrg(t, "Nobody's Account", &e.Rep1)
	e.WsExec(t, `UPDATE organization SET lifecycle = 'disqualified' WHERE id = $1`, idle)
	stale := accountMailDirectedAt(t, owner, e.WS, "Last try", "outbound", now.AddDate(0, 0, -60))
	linkToOrg(t, e, stale, idle)

	if written := ghostedScan(t, e, now); written != 0 {
		t.Errorf("the rule fired %d times on accounts it should ignore", written)
	}
}
