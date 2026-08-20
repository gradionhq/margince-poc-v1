// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The captured-organization auto-enrich lane end to end (ADR-0072/A118): a
// system-requested deep read APPLIES its findings directly (fill-empty, no
// confirm-first proposal) and records the sweep cursor terminal outcome; and
// the AutoEnrichStore's eligibility read + atomic daily cap behave over a real
// migrated Postgres.

import (
	"context"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestAutoEnrichLaneAppliesDirectlyInsteadOfStaging(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	store := capture.NewAutoEnrichStore(e.DB())
	worker, _ := newDeepReadTestWorker(e, acmeDeepSite(), acmeDeepBrain())

	// The dossier is created system-requested (as the sweep does), and its
	// cursor armed (MarkQueued) so the worker's terminal MarkResolved has a row.
	adminCtx := e.As(e.Rep1, nil, integration.AdminPerms)
	read, _, err := e.People.StartSiteRead(adminCtx, orgIDOf(org), seedURL, systemAutoEnrichActor)
	if err != nil {
		t.Fatalf("StartSiteRead: %v", err)
	}
	if err := store.MarkQueued(adminCtx, orgIDOf(org), 7*24*time.Hour); err != nil {
		t.Fatalf("MarkQueued: %v", err)
	}
	args := SiteDeepReadArgs{
		Workspace: e.WS, OrganizationID: org, SiteReadID: read.ID,
		RequestedBy: read.RequestedBy,
	}

	if err := worker.run(context.Background(), args); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The org fields + facts were applied directly — NOT staged as a deepread
	// proposal a human must accept.
	if n := deepReadApprovals(t, e); n != 0 {
		t.Fatalf("%d deepread proposals staged, want 0 — the auto lane applies directly", n)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM organization_profile_field WHERE organization_id = $1`, org); n == 0 {
		t.Fatal("the auto lane applied no profile fields")
	}
	if n := e.WsCount(t, `SELECT count(*) FROM organization_fact WHERE organization_id = $1`, org); n == 0 {
		t.Fatal("the auto lane applied no category facts")
	}
	// The sweep cursor is terminal: outcome 'applied', never re-enqueued.
	var outcome string
	var nextAttempt *time.Time
	if err := database.WithWorkspaceTx(adminCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT last_outcome, next_attempt_at FROM capture_auto_enrich_state WHERE organization_id = $1`,
			org).Scan(&outcome, &nextAttempt)
	}); err != nil {
		t.Fatalf("reading the cursor: %v", err)
	}
	if outcome != "applied" || nextAttempt != nil {
		t.Fatalf("cursor = (%q, %v), want (applied, <nil>)", outcome, nextAttempt)
	}
}

// insertDomainOrg seeds a captured, domain-named org (name_source='domain') with
// a live primary domain — the shape the sweep's ListDueOrgs considers.
func insertDomainOrg(t *testing.T, e *integration.Env, domain string) ids.OrganizationID {
	t.Helper()
	orgID := ids.NewV7()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `
			INSERT INTO organization (id, owner_id, display_name, name_source, source, captured_by)
			VALUES ($1, $2, $3, 'domain', 'connector:gmail', 'connector:gmail')`,
			orgID, e.Rep1, domain); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_domain (organization_id, domain, is_primary, source, captured_by)
			VALUES ($1, $2, true, 'connector:gmail', 'connector:gmail')`, orgID, domain)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ids.From[ids.OrganizationKind](orgID)
}

func TestAutoEnrichStoreEligibilityAndCap(t *testing.T) {
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.DB())
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)

	// How the company was NAMED no longer decides: a person creating one is
	// usually the moment they want the dossier, and the old name_source='domain'
	// rule had made this lane inert anyway (every org in the demo workspace is
	// human-named). What still excludes a company is having a dossier already.
	due1 := insertDomainOrg(t, e, "gitex.com")
	insertDomainOrg(t, e, "acme.example")
	insertOrg(t, e, e.Rep1, "human.example", "") // name_source='human' — now DUE
	// Give due1 a completed site read so it is excluded (already enriched).
	if _, _, err := e.People.StartSiteRead(ctx, due1, "https://gitex.com", "human:"+e.Rep1.String()); err != nil {
		t.Fatalf("seed dossier: %v", err)
	}

	dueList, err := store.ListDueOrgs(ctx, 10)
	if err != nil {
		t.Fatalf("ListDueOrgs: %v", err)
	}
	gotDomains := []string{}
	for _, d := range dueList {
		gotDomains = append(gotDomains, d.Domain)
	}
	sort.Strings(gotDomains)
	if !slices.Equal(gotDomains, []string{"acme.example", "human.example"}) {
		t.Fatalf("due = %v, want acme.example and human.example — a human-named company "+
			"is enriched too, and only the one holding a dossier is excluded", gotDomains)
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

// runReadTo takes one organization's read to a terminal status the way the
// worker does — start, claim, report — so the sweep sees the dossier state a
// real read leaves behind rather than a hand-written row.
func runReadTo(t *testing.T, e *integration.Env, orgID ids.OrganizationID, seedURL, status string) {
	t.Helper()
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	read, _, err := e.People.StartSiteRead(ctx, orgID, seedURL, systemAutoEnrichActor)
	if err != nil {
		t.Fatalf("start the read: %v", err)
	}
	if _, err := e.People.BeginSiteRead(ctx, read.ID, time.Minute); err != nil {
		t.Fatalf("claim the read: %v", err)
	}
	if err := e.People.FinishSiteRead(ctx, read.ID, people.FinishSiteReadInput{Status: status}); err != nil {
		t.Fatalf("report the read as %s: %v", status, err)
	}
}

func TestTheInstallationsOwnCompanyIsNeverSwept(t *testing.T) {
	// This test carries more weight than it used to. The anchor was excluded as
	// a SIDE EFFECT of being human-named, and the sweep no longer selects on
	// that — so `NOT o.is_anchor` is now the only thing keeping the company the
	// installation IS out of a lane that applies machine values directly. Its
	// own refresh compares each proposal against confirmed truth and asks the
	// human to resolve conflicts (ADR-0065 §8), and cold start has already read
	// it, so a sweep here would be both redundant and unreviewed.
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.DB())
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)

	website := "https://anchor.example"
	if _, err := e.People.SaveCompany(ctx, people.SaveCompanyInput{
		DisplayName: "Anchor", Website: &website,
	}); err != nil {
		t.Fatalf("describe the installation's own company: %v", err)
	}
	insertDomainOrg(t, e, "captured.example")

	due, err := store.ListDueOrgs(ctx, 10)
	if err != nil {
		t.Fatalf("ListDueOrgs: %v", err)
	}
	if len(due) != 1 || due[0].Domain != "captured.example" {
		t.Fatalf("due = %+v, want only the captured company — the anchor has a live primary domain too", due)
	}
}

func TestACancelledReadDoesNotRetireAnOrgForever(t *testing.T) {
	// A read cancelled because the operator had auto-enrich off when the worker
	// claimed it produced no dossier at all. Turning the setting back on has to
	// reach that company, or the sweep's self-healing stops at exactly the orgs
	// the setting stopped.
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.DB())
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)

	cancelled := insertDomainOrg(t, e, "offagain.example")
	enriched := insertDomainOrg(t, e, "enriched.example")
	runReadTo(t, e, cancelled, "https://offagain.example", "cancelled")
	runReadTo(t, e, enriched, "https://enriched.example", "done")

	due, err := store.ListDueOrgs(ctx, 10)
	if err != nil {
		t.Fatalf("ListDueOrgs: %v", err)
	}
	if len(due) != 1 || due[0].Domain != "offagain.example" {
		t.Fatalf("due = %+v, want the cancelled org offered again and the one holding a dossier left alone", due)
	}
}

func TestAutoEnrichExpireExhausted(t *testing.T) {
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.DB())
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
