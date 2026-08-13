// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The deep-read dossier: a human's start creates the queued row, a second
// start while one is in flight JOINS it (uq_site_read_inflight), the
// worker advances it queued → running → deferred or terminal through guarded CAS
// updates, and every read of it is scoped to the organization the caller
// can see.

import (
	"context"
	"errors"
	"testing"
	"time"

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
	store := people.NewStore(e.DB())
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
	store := people.NewStore(e.DB())
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
	claim, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute)
	if err != nil {
		t.Fatalf("BeginSiteRead: %v", err)
	}
	if claim.OrganizationID == nil || claim.SeedURL != read.SeedURL || *claim.OrganizationID != org.UUID {
		t.Fatalf("the claim reports %q/%s, want the dossier's own seed and org", claim.SeedURL, claim.OrganizationID)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); !errors.Is(err, apperrors.ErrNotFound) {
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

func TestSiteReadBudgetDeferralKeepsProgressAndJoinsUntilDue(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	worker := siteReadWorkerCtx(e)
	org := siteReadOrg(e.SeedOrg(t, "Acme", &e.Rep1))
	read, _, err := store.StartSiteRead(human, org, "https://acme.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	progressPages := []people.SiteReadPage{{URL: "https://acme.example", Kind: "home"}, {URL: "https://acme.example/imprint", Kind: "impressum"}}
	if err := store.UpdateSiteReadProgress(worker, read.ID, "extracting", progressPages); err != nil {
		t.Fatal(err)
	}
	next := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	if err := store.DeferSiteRead(worker, read.ID, next); err != nil {
		t.Fatal(err)
	}

	deferred, err := store.GetSiteRead(human, org, read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deferred.Status != "deferred" || deferred.StatusCode == nil || *deferred.StatusCode != "budget_deferred" ||
		deferred.StatusDetail == nil || deferred.NextAttemptAt == nil || !deferred.NextAttemptAt.Equal(next) {
		t.Fatalf("deferred dossier = %+v", deferred)
	}
	if deferred.PagesRead != 2 || len(deferred.Pages) != 2 || deferred.Pages[1].URL != "https://acme.example/imprint" || deferred.FinishedAt != nil {
		t.Fatalf("deferral discarded progress or became terminal: %+v", deferred)
	}
	joined, didJoin, err := store.StartSiteRead(human, org, read.SeedURL, "human:"+e.Rep2.String())
	if err != nil {
		t.Fatal(err)
	}
	if !didJoin || joined.ID != read.ID {
		t.Fatalf("start during deferral = (%s, %t), want join %s", joined.ID, didJoin, read.ID)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("claim before next_attempt_at: %v, want ErrNotFound", err)
	}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE site_read SET next_attempt_at = now() - interval '1 second' WHERE id = $1`, read.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); err != nil {
		t.Fatalf("claim due deferred read: %v", err)
	}
	resumed, err := store.GetSiteRead(human, org, read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "running" || resumed.StatusCode != nil || resumed.StatusDetail != nil || resumed.NextAttemptAt != nil || resumed.PagesRead != 2 {
		t.Fatalf("resumed dossier = %+v", resumed)
	}
}

func TestSiteReadWorkerReclaimsAStaleRunningDossier(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	worker := siteReadWorkerCtx(e)
	org := siteReadOrg(e.SeedOrg(t, "Acme", &e.Rep1))
	read, _, err := store.StartSiteRead(human, org, "https://acme.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 20*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE site_read
			SET started_at = now() - interval '11 minutes' WHERE id = $1`, read.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 20*time.Minute); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("reclaim before configured timeout: %v, want ErrNotFound", err)
	}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE site_read
			SET started_at = now() - interval '21 minutes' WHERE id = $1`, read.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := store.BeginSiteRead(worker, read.ID, 20*time.Minute)
	if err != nil {
		t.Fatalf("reclaim stale running dossier: %v", err)
	}
	if claim.OrganizationID == nil || *claim.OrganizationID != org.UUID {
		t.Fatalf("reclaimed target = %v, want %s", claim.OrganizationID, org)
	}
}

func TestSiteReadIsScopedToTheOrganizationTheCallerCanSee(t *testing.T) {
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
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

func TestATransientlyFailedReadIsClaimedAgainWhenItsRetryFallsDue(t *testing.T) {
	// A 403 from an edge's bot protection is not the site's final answer, so the
	// failure names a time to try again. Without the claim honoring it the retry
	// time would be state nothing reads, and one bad minute would settle a live
	// company's site for ever.
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	worker := siteReadWorkerCtx(e)
	org := siteReadOrg(e.SeedOrg(t, "Surfe", &e.Rep1))
	read, _, err := store.StartSiteRead(human, org, "https://surfe.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	next := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	if err := store.FinishSiteRead(worker, read.ID, people.FinishSiteReadInput{
		Status:        "failed",
		StatusCode:    "bot_blocked",
		StatusDetail:  "The site answered 403 — bot protection refused the read.",
		NextAttemptAt: &next,
	}); err != nil {
		t.Fatalf("finish as bot_blocked: %v", err)
	}

	failed, err := store.GetSiteRead(human, org, read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.StatusCode == nil || *failed.StatusCode != "bot_blocked" || failed.StatusDetail == nil {
		t.Fatalf("failed dossier lost its diagnosis: %+v", failed)
	}
	if failed.NextAttemptAt == nil || !failed.NextAttemptAt.Equal(next) {
		t.Fatalf("failed dossier did not keep its retry time: %+v", failed)
	}

	// Not yet due: the failure stands.
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("claim before next_attempt_at: %v, want ErrNotFound", err)
	}
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE site_read SET next_attempt_at = now() - interval '1 second' WHERE id = $1`, read.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); err != nil {
		t.Fatalf("claim due failed read: %v", err)
	}
	retried, err := store.GetSiteRead(human, org, read.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != "running" || retried.StatusCode != nil || retried.NextAttemptAt != nil {
		t.Fatalf("retried dossier = %+v, want a clean running claim", retried)
	}
}

func TestAPermanentlyFailedReadIsNeverClaimedAgain(t *testing.T) {
	// A domain that does not resolve will not resolve tomorrow either. It sets
	// no retry time, and nothing may re-claim it — re-crawling those is the
	// noise the retry arm must not create.
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	worker := siteReadWorkerCtx(e)
	org := siteReadOrg(e.SeedOrg(t, "Nowhere", &e.Rep1))
	read, _, err := store.StartSiteRead(human, org, "https://nowhere.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSiteRead(worker, read.ID, people.FinishSiteReadInput{
		Status:       "failed",
		StatusCode:   "dns",
		StatusDetail: "The domain name does not resolve to a server.",
	}); err != nil {
		t.Fatalf("finish as dns: %v", err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("claim of a terminal failure: %v, want ErrNotFound", err)
	}
}

func TestASucceededReadCarriesNoDiagnosis(t *testing.T) {
	// The columns say what went wrong. A read that worked has nothing to say
	// there, and the store refuses the contradiction rather than storing it.
	e := integration.Setup(t)
	store := people.NewStore(e.DB())
	human := e.As(e.Rep1, nil, integration.AdminPerms)
	worker := siteReadWorkerCtx(e)
	org := siteReadOrg(e.SeedOrg(t, "Fine", &e.Rep1))
	read, _, err := store.StartSiteRead(human, org, "https://fine.example", "human:"+e.Rep1.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginSiteRead(worker, read.ID, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	err = store.FinishSiteRead(worker, read.ID, people.FinishSiteReadInput{
		Status: "done", StatusCode: "tls", StatusDetail: "not a failure",
	})
	if err == nil {
		t.Fatal("a done read accepted a failure diagnosis")
	}
	if err := store.FinishSiteRead(worker, read.ID, people.FinishSiteReadInput{
		Status: "failed", StatusCode: "bot_blocked",
	}); err == nil {
		t.Fatal("a failure was accepted with no sentence a human can act on")
	}
}
