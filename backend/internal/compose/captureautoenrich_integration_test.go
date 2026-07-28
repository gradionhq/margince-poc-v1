// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The captured-organization auto-enrich lane end to end (ADR-0072/A118): a
// system-requested deep read STAGES its findings as a confirm-first proposal
// and writes nothing to the organization, and it records the sweep cursor
// terminal outcome; and the AutoEnrichStore's eligibility read + atomic daily
// cap behave over a real migrated Postgres.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// startAutoEnrichDossier creates the dossier system-requested (as the sweep
// does) and arms its cursor (MarkQueued), so the worker's terminal MarkResolved
// has a row to land on. It returns the job args the sweep would enqueue.
func startAutoEnrichDossier(t *testing.T, e *integration.Env, org ids.UUID) SiteDeepReadArgs {
	t.Helper()
	adminCtx := e.As(e.Rep1, nil, integration.AdminPerms)
	read, _, err := e.People.StartSiteRead(adminCtx, orgIDOf(org), seedURL, systemAutoEnrichActor)
	if err != nil {
		t.Fatalf("StartSiteRead: %v", err)
	}
	if err := capture.NewAutoEnrichStore(e.Pool).MarkQueued(adminCtx, orgIDOf(org), 7*24*time.Hour); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	return SiteDeepReadArgs{
		WorkspaceID: e.WS, OrganizationID: org, SiteReadID: read.ID,
		SeedURL: read.SeedURL, RequestedBy: read.RequestedBy,
	}
}

// autoEnrichCursor reads one org's sweep cursor. A nil next_attempt_at means
// the outcome retired the org — it has left the due index for good.
func autoEnrichCursor(t *testing.T, e *integration.Env, org ids.UUID) (string, *time.Time) {
	t.Helper()
	var outcome string
	var nextAttempt *time.Time
	if err := database.WithWorkspaceTx(e.As(e.Rep1, nil, integration.AdminPerms), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT last_outcome, next_attempt_at FROM capture_auto_enrich_state WHERE organization_id = $1`,
			org).Scan(&outcome, &nextAttempt)
	}); err != nil {
		t.Fatalf("reading the cursor: %v", err)
	}
	return outcome, nextAttempt
}

func TestAutoEnrichLaneStagesAndWritesNothingToTheOrganization(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	worker, _ := newDeepReadTestWorker(e, acmeDeepSite(), acmeDeepBrain())
	args := startAutoEnrichDossier(t, e, org)

	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The org fields + facts are STAGED as one deepread proposal a human must
	// accept — the site's claims about this company reach nobody's records on
	// the model's say-so alone.
	if n := deepReadApprovals(t, e); n != 1 {
		t.Fatalf("%d deepread proposals staged, want 1 — the auto lane stages like the human lane", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM organization_profile_field WHERE organization_id = $1`, org); n != 0 {
		t.Fatalf("%d profile fields written, want 0 — the auto lane must write nothing before the accept", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM organization_fact WHERE organization_id = $1`, org); n != 0 {
		t.Fatalf("%d category facts written, want 0 — the auto lane must write nothing before the accept", n)
	}
	// The sweep cursor is terminal: outcome 'staged', never re-enqueued.
	if outcome, nextAttempt := autoEnrichCursor(t, e, org); outcome != "staged" || nextAttempt != nil {
		t.Fatalf("cursor = (%q, %v), want (staged, <nil>)", outcome, nextAttempt)
	}
}

// emptySiteBrain answers both lanes with nothing found, for a one-page site.
func emptySiteBrain() laneFake {
	return laneFake{
		profileReply: `{"fields":[]}`,
		pageReplies:  map[string]string{seedURL: `{"facts":[],"people":[]}`},
	}
}

func TestAutoEnrichLaneFillsTheEmployeeItKnowsAndStagesTheStranger(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	// Anna already works here and the CRM has her address but not her title;
	// Bernd is a stranger. The team page prints both.
	anna := seatEmployee(t, e, org, "Anna Muster", "anna@acme.example")
	worker, _ := newDeepReadTestWorker(e, acmeTeamSite(), teamDeepBrain())
	args := startAutoEnrichDossier(t, e, org)

	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Anna is not a stranger, so she is not offered back as a duplicate lead —
	// the site's role fills the empty column on the record that exists.
	title := seatedTitle(t, e, anna)
	if title == nil || *title != "Chief Executive Officer" {
		t.Fatalf("Anna's title = %v, want the role her company's site prints", title)
	}
	// Bernd is a stranger and stays staged (NEVER-8): exactly one lead, his.
	leads := e.WsCount(t, `SELECT count(*) FROM approval WHERE kind = 'site_lead'`)
	if leads != 1 {
		t.Fatalf("%d site_lead proposals, want 1 — the known employee must not be re-offered as a lead", leads)
	}
	if outcome, nextAttempt := autoEnrichCursor(t, e, org); outcome != "staged" || nextAttempt != nil {
		t.Fatalf("cursor = (%q, %v), want (staged, <nil>)", outcome, nextAttempt)
	}
}

func TestAutoEnrichLaneRecordsAnHonestEmptyReadAsTerminal(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	worker, _ := newDeepReadTestWorker(e,
		&fakeSite{pages: map[string]fakeSitePage{seedURL: {text: readable("Acme home.")}}},
		emptySiteBrain())
	args := startAutoEnrichDossier(t, e, org)

	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	if n := e.WsCount(t, `SELECT count(*) FROM approval`); n != 0 {
		t.Fatalf("%d approvals staged, want 0 — a site that evidenced nothing must ask nobody anything", n)
	}
	// 'empty' retires the org exactly like 'staged': the read did its whole job,
	// so re-reading the same site would spend the daily cap to learn nothing.
	if outcome, nextAttempt := autoEnrichCursor(t, e, org); outcome != "empty" || nextAttempt != nil {
		t.Fatalf("cursor = (%q, %v), want (empty, <nil>)", outcome, nextAttempt)
	}
}

// deadPool is a pool that will never serve a query: every statement through it
// fails the way a database outage fails. It forces the two faults below without
// breaking the database the rest of the test still needs.
func deadPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), os.Getenv("MARGINCE_TEST_APP_DSN"))
	if err != nil {
		t.Fatalf("building the pool: %v", err)
	}
	pool.Close()
	return pool
}

func TestAutoEnrichLaneLeavesTheCursorArmedWhenStagingFaults(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	worker, _ := newDeepReadTestWorker(e, acmeDeepSite(), acmeDeepBrain())
	worker.approvals = approvals.NewService(deadPool(t))
	args := startAutoEnrichDossier(t, e, org)

	if err := worker.run(context.Background(), args); err == nil {
		t.Fatal("run succeeded with a dead approvals store — a read whose findings reached nobody must fail loudly")
	}

	// 'failed' is the one outcome that does NOT retire the org: MarkQueued's
	// backoff still stands, so the next due sweep retries instead of writing
	// this company off on one database fault.
	outcome, nextAttempt := autoEnrichCursor(t, e, org)
	if outcome != "failed" || nextAttempt == nil {
		t.Fatalf("cursor = (%q, %v), want (failed, <a retry time>)", outcome, nextAttempt)
	}
}

func TestAutoEnrichLaneFinishesTheReadWhenTheCursorWriteFaults(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	worker, _ := newDeepReadTestWorker(e, acmeDeepSite(), acmeDeepBrain())
	worker.autoEnrich = capture.NewAutoEnrichStore(deadPool(t))
	args := startAutoEnrichDossier(t, e, org)

	// The cursor is the SWEEP's bookkeeping, never the authority on the read.
	// Losing the write costs at most one reconsidered org next pass, which the
	// dossier-exists gate then filters out — so it must not fail a read whose
	// proposals are already staged and waiting for a human.
	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v — a lost cursor write must not fail a read that staged its findings", err)
	}
	if n := deepReadApprovals(t, e); n != 1 {
		t.Fatalf("%d deepread proposals staged, want 1 — the read's findings must survive the cursor fault", n)
	}
	done, err := e.People.GetSiteRead(e.As(e.Rep1, nil, integration.AdminPerms), orgIDOf(org), args.SiteReadID)
	if err != nil {
		t.Fatalf("GetSiteRead: %v", err)
	}
	if done.Status != "done" {
		t.Fatalf("dossier status = %q, want done", done.Status)
	}
}

// insertDomainOrg seeds a captured, domain-named org (name_source='domain') with
// a live primary domain — the shape the sweep's ListDueOrgs considers.
func insertDomainOrg(t *testing.T, e *integration.Env, domain string) ids.OrganizationID {
	t.Helper()
	orgID := ids.NewV7()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, workspace_id, owner_id, display_name, name_source, source, captured_by)
			VALUES ($1, $2, $3, $4, 'domain', 'connector:gmail', 'connector:gmail')`,
			orgID, e.WS, e.Rep1, domain); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_domain (workspace_id, organization_id, domain, is_primary, source, captured_by)
			VALUES ($1, $2, $3, true, 'connector:gmail', 'connector:gmail')`, e.WS, orgID, domain)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ids.From[ids.OrganizationKind](orgID)
}

func TestAutoEnrichStoreEligibilityAndCap(t *testing.T) {
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.Pool)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)

	// Two captured domain-named orgs are due; a human-named org (from insertOrg,
	// name_source='human') is not; nor is one that already has a dossier.
	due1 := insertDomainOrg(t, e, "gitex.com")
	insertDomainOrg(t, e, "acme.example")
	humanOrg := insertOrg(t, e, e.Rep1, "human.example", "") // name_source='human'
	_ = humanOrg
	// Give due1 a completed site read so it is excluded (already enriched).
	if _, _, err := e.People.StartSiteRead(ctx, due1, "https://gitex.com", "human:"+e.Rep1.String()); err != nil {
		t.Fatalf("seed dossier: %v", err)
	}

	dueList, err := store.ListDueOrgs(ctx, 10)
	if err != nil {
		t.Fatalf("ListDueOrgs: %v", err)
	}
	if len(dueList) != 1 || dueList[0].Domain != "acme.example" {
		t.Fatalf("due = %+v, want exactly acme.example (human-named excluded, dossier'd excluded)", dueList)
	}

	// The daily cap is atomic: with a cap of 2, the first two reservations
	// succeed and the third is refused.
	got := []bool{}
	for range 3 {
		slot, err := store.ReserveBudget(ctx, 2)
		if err != nil {
			t.Fatalf("ReserveBudget: %v", err)
		}
		got = append(got, slot.Reserved)
	}
	if got[0] != true || got[1] != true || got[2] != false {
		t.Fatalf("reservations = %v, want [true true false] at cap 2", got)
	}
}

func TestAutoEnrichExpireExhausted(t *testing.T) {
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.Pool)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	org := insertDomainOrg(t, e, "fail.example")

	// Two attempts used (backoff 0 so the cursor stays due, not future-armed):
	// at the attempt bound it is no longer a candidate...
	for range 2 {
		if err := store.MarkQueued(ctx, org, 0); err != nil {
			t.Fatalf("MarkQueued: %v", err)
		}
	}
	due, err := store.ListDueOrgs(ctx, 10)
	if err != nil {
		t.Fatalf("ListDueOrgs: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("due = %+v, want none — the org used every attempt", due)
	}

	// ...and the per-pass expiry retires it: outcome 'exhausted', cursor cleared
	// so it leaves the due index.
	if err := store.ExpireExhausted(ctx); err != nil {
		t.Fatalf("ExpireExhausted: %v", err)
	}
	var outcome string
	var nextAttempt *time.Time
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT last_outcome, next_attempt_at FROM capture_auto_enrich_state WHERE organization_id = $1`,
			org).Scan(&outcome, &nextAttempt)
	}); err != nil {
		t.Fatalf("reading the cursor: %v", err)
	}
	if outcome != "exhausted" || nextAttempt != nil {
		t.Fatalf("cursor = (%q, %v), want (exhausted, <nil>)", outcome, nextAttempt)
	}
}
