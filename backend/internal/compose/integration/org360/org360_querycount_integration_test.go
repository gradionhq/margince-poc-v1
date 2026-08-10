// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package org360

// The 360 exists to replace a stack of round trips with one. That promise
// is only true if the assembly's cost is flat in the size of the account:
// a per-contact strength read, or a per-deal stage lookup, would turn one
// HTTP call into N database calls and quietly reintroduce exactly what the
// endpoint was built to remove.
//
// So this counts queries rather than milliseconds. A timing assertion on a
// shared CI box measures the box; a query count measures the code, and it
// is the number that actually degrades when someone adds a loop.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	org360svc "github.com/gradionhq/margince/backend/internal/compose/org360"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// countingTracer counts the statements one pool issues, ignoring the
// transaction and GUC plumbing every workspace transaction pays once —
// what is under test is how the section reads scale, not BEGIN.
type countingTracer struct {
	mu    sync.Mutex
	count int
}

func (t *countingTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sql := strings.TrimSpace(strings.ToUpper(data.SQL))
	if strings.HasPrefix(sql, "BEGIN") || strings.HasPrefix(sql, "COMMIT") ||
		strings.HasPrefix(sql, "ROLLBACK") || strings.HasPrefix(sql, "SELECT SET_CONFIG") {
		return ctx
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.count++
	return ctx
}

func (t *countingTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (t *countingTracer) total() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// tracedPool opens a second app-role pool that counts its statements.
func tracedPool(t *testing.T, tracer *countingTracer) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MARGINCE_TEST_APP_DSN")
	if dsn == "" {
		t.Fatal("MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = tracer
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		database.RegisterIDTypes(conn)
		return nil
	}
	traced, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	return traced
}

func TestOrganization360CostDoesNotGrowWithTheAccount(t *testing.T) {
	e := integration.Setup(t)
	owner := integration.OwnerConn(t)

	// One pipeline for both accounts: the fixture seeds the workspace
	// default, and seeding it twice is a conflict, not a second pipeline.
	pipeline, stage, _ := integration.DealFixture(t, e)
	small := seedAccount(t, e, owner, "Small Account", 1, pipeline, stage)
	large := seedAccount(t, e, owner, "Large Account", 12, pipeline, stage)

	tracer := &countingTracer{}
	pool := tracedPool(t, tracer)
	svc := org360svc.NewService(pool, people.NewStore(pool), approvals.NewService(pool),
		func() time.Time { return org360Clock })
	ctx := e.Admin()

	before := tracer.total()
	if _, err := svc.Assemble(ctx, small); err != nil {
		t.Fatalf("assemble the small account: %v", err)
	}
	smallCost := tracer.total() - before

	before = tracer.total()
	if _, err := svc.Assemble(ctx, large); err != nil {
		t.Fatalf("assemble the large account: %v", err)
	}
	largeCost := tracer.total() - before

	if largeCost != smallCost {
		t.Errorf("assembling an account with 12 contacts and 12 deals cost %d queries, one with 1 cost %d — "+
			"the composite read must be flat in the size of the account, or it is the round-trip stack it replaced",
			largeCost, smallCost)
	}
	// A ceiling as well as a shape: ten sections, a handful of queries each
	// at most. If this ever needs raising, the reason belongs in the commit
	// that raises it.
	//
	// Raised 25 → 27 for the last_touch section (ADR-0079 arc): it is a new
	// section, and it costs its own grant check and its own read. Two rather
	// than one because the read is gated like every other section — a caller
	// without the activity grant gets silence, not a timestamp.
	//
	// Raised 27 → 28 for next_meeting: ONE query, not the two it would take to
	// read the meeting and then its attendees. The attendees arrive as JSON
	// from a lateral sub-select carrying their own row-scope predicate, so the
	// section stays flat in the size of the meeting as well as the account. It
	// reuses the activity grant the timeline already checked, so it costs no
	// second admission.
	//
	// Raised 28 → 29 for the contacts' internal routes: ONE query for the whole
	// contact set, not one per contact, which is the difference between a
	// composite that costs the same on every account and one that costs most on
	// the accounts with the most contacts. It rides the person grant the
	// contacts section already checked, so it costs no second admission.
	//
	// Raised 29 → 30 for last_meeting_at: ONE query, the most recent meeting
	// that has already happened. It is the opposite question from the
	// next-meeting section — backwards rather than forwards — so it cannot be
	// read off that section's row, and it carries the activity scope clause
	// itself rather than trusting a read that asked something else. It reuses
	// the activity grant the timeline already checked, so it costs no second
	// admission, and it stays flat in the size of the account like every
	// other section here.
	//
	// It landed without this line, so the assembly issued 30 against a budget
	// of 29 and every branch cut afterwards inherited a red gate.
	const budget = 30
	if smallCost > budget {
		t.Errorf("one 360 issued %d queries, budget is %d", smallCost, budget)
	}
}

// seedAccount builds one organization with n employed contacts (each with
// an interaction, so strength is real work) and n open deals.
func seedAccount(t *testing.T, e *integration.Env, owner *pgx.Conn, name string, n int,
	pipeline ids.PipelineID, stage ids.StageID,
) ids.OrganizationID {
	t.Helper()
	org := e.SeedOrg(t, name, &e.Rep1)
	for range n {
		contact := e.SeedPerson(t, name+" contact", &e.Rep1)
		e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, organization_id, source, captured_by)
			VALUES ($1, 'employment', $2, $3, 'manual', 'human:x')`, e.WS, contact, org)
		activity := integration.SeedRow(t, owner, `INSERT INTO activity (id, workspace_id, kind, subject, occurred_at, direction, source, captured_by)
			VALUES ($1, $2, 'email', 'touch', '2026-05-28T09:00:00Z', 'inbound', 'manual', 'human:x')`, e.WS)
		integration.LinkActivity(t, owner, e.WS, activity, "person", contact)

		deal := e.SeedDeal(t, name+" deal", pipeline, stage, &e.Rep1)
		e.WsExec(t, `UPDATE deal SET organization_id = $2 WHERE id = $1`, deal, org)
		e.WsExec(t, `INSERT INTO relationship (workspace_id, kind, person_id, deal_id, role, source, captured_by)
			VALUES ($1, 'deal_stakeholder', $2, $3, 'champion', 'manual', 'human:x')`, e.WS, contact, deal)
	}
	return ids.From[ids.OrganizationKind](org)
}
