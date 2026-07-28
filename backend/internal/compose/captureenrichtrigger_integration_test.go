// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The enrich-on-capture trigger's decisions (ADR-0072/A118 §9).
//
// What these cover is the part that can go wrong quietly: which gates run, in
// which order, and — for every way the trigger gives up — whether the
// organization is left in a state the daily sweep still finds. That contract is
// the entire reason the trigger is allowed to be best-effort, so it is the thing
// worth holding.
//
// What they do NOT cover is the queued read itself: starting one needs an
// ambient River client, and this repo has no harness that stands one up in a
// test. The same gap already applies to the sweep's own trigger path; the
// read-and-apply half is covered by TestAutoEnrichLaneAppliesDirectlyInsteadOfStaging.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// stillDue reports whether the sweep would pick this organization up.
func stillDue(t *testing.T, e *integration.Env, org ids.OrganizationID) bool {
	t.Helper()
	due, err := capture.NewAutoEnrichStore(e.Pool).ListDueOrgs(e.Admin(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		if d.OrganizationID == org {
			return true
		}
	}
	return false
}

func setAutoEnrich(t *testing.T, e *integration.Env, on bool) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE workspace SET capture_auto_enrich = $2 WHERE id = $1`, e.WS, on)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func budgetSpent(t *testing.T, e *integration.Env) int {
	t.Helper()
	return e.WsCount(t, `
		SELECT coalesce(sum(enqueued), 0) FROM capture_auto_enrich_budget`)
}

func TestEnrichOnCaptureRespectsTheSettingBeforeSpendingAnything(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, false)
	org := insertDomainOrg(t, e, "switched-off.example")

	trigger := newAutoEnrichTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.organizationCaptured(e.Admin(), org, "switched-off.example")

	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("%d budget slots spent with the setting off — the flag must be read before the budget", n)
	}
	if !stillDue(t, e, org) {
		t.Fatal("the organization was retired from the sweep by a trigger that did nothing")
	}
}

func TestEnrichOnCaptureLeavesTheOrgToTheSweepAtTheDailyCap(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	org := insertDomainOrg(t, e, "capped.example")

	// Spend the day's reads through the same reservation the sweep uses, so the
	// trigger meets a genuinely exhausted budget rather than a mocked one.
	store := capture.NewAutoEnrichStore(e.Pool)
	for i := 0; i < autoEnrichDailyCap; i++ {
		reserved, err := store.ReserveBudget(e.Admin(), autoEnrichDailyCap)
		if err != nil {
			t.Fatal(err)
		}
		if !reserved {
			t.Fatalf("reservation %d refused before the cap", i)
		}
	}

	trigger := newAutoEnrichTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.organizationCaptured(e.Admin(), org, "capped.example")

	if n := budgetSpent(t, e); n != autoEnrichDailyCap {
		t.Fatalf("budget spent = %d, want the cap %d — the trigger must not spend past it", n, autoEnrichDailyCap)
	}
	if !stillDue(t, e, org) {
		t.Fatal("a capped trigger retired the organization — it must stay due for a later sweep")
	}
}

// Every way the trigger can give up has to leave the organization findable by
// the sweep. This drives the give-up that is hardest to reason about — the read
// itself failing to start — by running with no ambient River client, which is
// exactly what a missing queue looks like from here.
func TestEnrichOnCaptureLeavesTheOrgToTheSweepWhenTheReadCannotStart(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	org := insertDomainOrg(t, e, "no-queue.example")

	trigger := newAutoEnrichTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.organizationCaptured(e.Admin(), org, "no-queue.example")

	if !stillDue(t, e, org) {
		t.Fatal("a trigger that could not start the read retired the organization anyway")
	}
	// The reserved slot is spent with no read to show for it. That is the
	// deliberate direction: under-spending the day's budget costs a dossier a
	// few hours, over-spending it costs money the cap exists to bound.
	if n := budgetSpent(t, e); n != 1 {
		t.Fatalf("budget spent = %d, want 1 — the reservation precedes the start and is not returned", n)
	}
}

// There is nothing to read without a domain, and the cheapest gate runs first:
// no setting lookup, no reservation, no log line.
func TestEnrichOnCaptureIgnoresAnEmptyDomain(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	org := insertDomainOrg(t, e, "nodomain.example")

	trigger := newAutoEnrichTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.organizationCaptured(e.Admin(), org, "")

	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("%d budget slots spent for an organization with no domain to read", n)
	}
}
