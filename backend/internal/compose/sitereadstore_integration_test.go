// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The deep-read dossier: a human's start creates the queued row, a second
// start while one is in flight JOINS it (uq_site_read_inflight), the
// worker advances it queued → running → terminal through guarded CAS
// updates, and every read of it is scoped to the organization the caller
// can see.

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// siteReadOrg types a harness-seeded untyped org id for the store calls.
func siteReadOrg(u ids.UUID) ids.OrganizationID { return ids.From[ids.OrganizationKind](u) }

// siteReadWorkerCtx is the worker's context shape: the job binds the
// workspace (RLS), no human principal — exactly what Begin/Finish run under.
func siteReadWorkerCtx(e *integration.Env) context.Context {
	return principal.WithWorkspaceID(context.Background(), e.WS)
}

func TestSiteReadStartCreatesAQueuedDossierAndAReClickJoinsIt(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	ctx := e.As(e.Rep1, nil, integration.AdminPerms)
	org := siteReadOrg(e.SeedOrg(t, "Acme", &e.Rep1))

	first, joined, err := store.StartSiteRead(ctx, org, "https://acme.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatalf("StartSiteRead: %v", err)
	}
	if joined {
		t.Fatal("the first start reports joined — there was nothing to join")
	}
	if first.Status != "queued" || first.SeedURL != "https://acme.example" {
		t.Fatalf("started read = %+v, want a queued dossier for the seed url", first)
	}

	// The SPA's poll sees the queued row.
	got, err := store.GetSiteRead(ctx, org, first.ID)
	if err != nil {
		t.Fatalf("GetSiteRead: %v", err)
	}
	if got.ID != first.ID || got.Status != "queued" || got.StartedAt != nil || got.FinishedAt != nil {
		t.Fatalf("polled read = %+v, want the queued dossier untouched by any worker", got)
	}

	// Re-clicking while the read is in flight joins it: same id, no rival row.
	second, joined, err := store.StartSiteRead(ctx, org, "https://acme.example", "human:"+e.Rep2.String())
	if err != nil {
		t.Fatalf("second StartSiteRead: %v", err)
	}
	if !joined || second.ID != first.ID {
		t.Fatalf("second start = (id %s, joined %t), want to join %s", second.ID, joined, first.ID)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM site_read WHERE organization_id = $1`, org); n != 1 {
		t.Fatalf("re-clicking created %d dossiers, want the one in flight", n)
	}
}

func TestSiteReadWorkerAdvancesTheDossierThroughGuardedTransitions(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	worker := siteReadWorkerCtx(e)
	org := siteReadOrg(e.SeedOrg(t, "Acme", &e.Rep1))

	read, _, err := store.StartSiteRead(human, org, "https://acme.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatalf("StartSiteRead: %v", err)
	}

	// The pickup is a CAS: the first Begin flips queued → running and hands
	// back the claimed row's own identity; a second worker claiming the same
	// read is told there is nothing to begin.
	claim, err := store.BeginSiteRead(worker, read.ID)
	if err != nil {
		t.Fatalf("BeginSiteRead: %v", err)
	}
	if claim.OrganizationID == nil || claim.SeedURL != read.SeedURL || *claim.OrganizationID != org.UUID {
		t.Fatalf("the claim reports %q/%s, want the dossier's own seed and org", claim.SeedURL, claim.OrganizationID)
	}
	if _, err := store.BeginSiteRead(worker, read.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("second BeginSiteRead → %v, want ErrNotFound (the read is no longer queued)", err)
	}
	running, err := store.GetSiteRead(human, org, read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.Status != "running" || running.StartedAt == nil {
		t.Fatalf("after Begin the read is %+v, want running with started_at stamped", running)
	}

	// Finish records the whole crawl report in one terminal write.
	stopped := "page_cap"
	proposal := ids.NewV7()
	err = store.FinishSiteRead(worker, read.ID, people.FinishSiteReadInput{
		Status: "partial",
		Pages: []people.SiteReadPage{
			{URL: "https://acme.example/", Kind: "home"},
			{URL: "https://acme.example/impressum", Kind: "impressum"},
		},
		Skipped:       []people.SiteReadSkip{{URL: "https://acme.example/blog", Reason: "robots"}},
		StoppedReason: &stopped,
		FactCount:     7,
		ProposalIDs:   []ids.UUID{proposal},
	})
	if err != nil {
		t.Fatalf("FinishSiteRead: %v", err)
	}
	done, err := store.GetSiteRead(human, org, read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "partial" || done.FinishedAt == nil || done.FactCount != 7 {
		t.Fatalf("finished read = %+v, want partial with fact_count 7 and finished_at stamped", done)
	}
	if len(done.Pages) != 2 || done.Pages[1].Kind != "impressum" ||
		len(done.Skipped) != 1 || done.Skipped[0].Reason != "robots" {
		t.Fatalf("crawl report did not round-trip: pages %+v skipped %+v", done.Pages, done.Skipped)
	}
	if done.StoppedReason == nil || *done.StoppedReason != "page_cap" {
		t.Fatalf("stopped_reason = %v, want page_cap", done.StoppedReason)
	}
	if len(done.ProposalIDs) != 1 || done.ProposalIDs[0] != proposal {
		t.Fatalf("proposal_ids = %v, want [%s]", done.ProposalIDs, proposal)
	}

	// The terminal write is a CAS too: a finished read cannot finish again.
	if err := store.FinishSiteRead(worker, read.ID, people.FinishSiteReadInput{Status: "done"}); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("second FinishSiteRead → %v, want ErrNotFound (the read is no longer running)", err)
	}

	// The in-flight uniqueness covers only queued/running: with the read
	// finished, a fresh start mints a NEW dossier instead of joining a done one.
	again, joined, err := store.StartSiteRead(human, org, "https://acme.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatalf("StartSiteRead after finish: %v", err)
	}
	if joined || again.ID == read.ID {
		t.Fatalf("a start after the read finished joined the finished dossier (id %s, joined %t)", again.ID, joined)
	}
}

func TestSiteReadWorkerReclaimsAStaleRunningDossier(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	worker := siteReadWorkerCtx(e)
	org := siteReadOrg(e.SeedOrg(t, "Acme", &e.Rep1))
	read, _, err := store.StartSiteRead(human, org, "https://acme.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID); err != nil {
		t.Fatal(err)
	}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE site_read
			SET started_at = now() - interval '11 minutes' WHERE id = $1`, read.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.BeginSiteRead(worker, read.ID)
	if err != nil {
		t.Fatalf("reclaim stale running dossier: %v", err)
	}
	if claim.OrganizationID == nil || *claim.OrganizationID != org.UUID {
		t.Fatalf("reclaimed target = %v, want %s", claim.OrganizationID, org)
	}
}

func TestSiteReadIsScopedToTheOrganizationTheCallerCanSee(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.Pool)
	admin := e.As(e.Rep1, nil, integration.AdminPerms)
	orgA := siteReadOrg(e.SeedOrg(t, "Org A", &e.Rep1))
	orgB := siteReadOrg(e.SeedOrg(t, "Org B", &e.Rep1))

	read, _, err := store.StartSiteRead(admin, orgA, "https://a.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatalf("StartSiteRead: %v", err)
	}

	// A read id fetched under the WRONG organization is a 404: the dossier
	// is addressed through its org, never as a free-floating id.
	if _, err := store.GetSiteRead(admin, orgB, read.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("GetSiteRead under another org → %v, want ErrNotFound", err)
	}

	// An org outside the caller's row scope is invisible: starting a read
	// on it is the existence-hiding 404, not a permission error.
	foreign := siteReadOrg(e.SeedOrg(t, "Rep3's Org", &e.Rep3))
	rep := e.As(e.Rep1, []ids.UUID{e.Team1}, principal.Permissions{
		RoleKeys: []string{"rep"},
		Objects: map[string]principal.ObjectGrant{
			"organization": {Create: true, Read: true, Update: true},
		},
		RowScope: principal.RowScopeTeam,
	})
	if _, _, err := store.StartSiteRead(rep, foreign, "https://foreign.example", "human:"+e.Rep1.String()); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("StartSiteRead on an invisible org → %v, want ErrNotFound", err)
	}
	if _, err := store.GetSiteRead(rep, foreign, read.ID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("GetSiteRead on an invisible org → %v, want ErrNotFound", err)
	}
}
