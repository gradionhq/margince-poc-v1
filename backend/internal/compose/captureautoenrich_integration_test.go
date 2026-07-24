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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestAutoEnrichLaneAppliesDirectlyInsteadOfStaging(t *testing.T) {
	e := integration.Setup(t)
	org := insertOrg(t, e, e.Rep1, "acme.example", "")
	store := capture.NewAutoEnrichStore(e.Pool)
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
		WorkspaceID: e.WS, OrganizationID: org, SiteReadID: read.ID,
		SeedURL: read.SeedURL, RequestedBy: read.RequestedBy,
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
		reserved, err := store.ReserveBudget(ctx, 2)
		if err != nil {
			t.Fatalf("ReserveBudget: %v", err)
		}
		got = append(got, reserved)
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
