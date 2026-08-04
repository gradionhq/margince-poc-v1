// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// declaredTimeout is what River will be handed for a kind, resolved exactly as
// addGovernedWorker resolves it. Reading the Spec rather than a worker is the
// point of this commit: no worker answers the question any more.
func declaredTimeout(t *testing.T, kind string, supplied time.Duration) time.Duration {
	t.Helper()
	spec, ok := jobs.SpecFor(kind)
	if !ok {
		t.Fatalf("%s is not declared in api/jobs.yaml", kind)
	}
	return spec.Timeout.Duration(supplied)
}

// TestTheTwoPassesDeclaredWithNoWallClockStillResolveToNegativeOne pins the
// behaviour the deleted Timeout methods used to hold. A NEGATIVE River timeout
// is not "a long time": job_rescuer.go ignores a stuck job whose worker
// declares one, at any age, which is the wedge search.ReembedClaim.StealAfter
// exists to answer. A {none: true} declaration that resolved to 0 instead
// would restore River's one-minute default on the two longest passes in the
// tree and re-arm the rescuer behind them, and neither change would be visible
// anywhere else.
//
// It is the WORKSPACE half that holds it in both pairs — a dispatcher only
// enqueues, and an unbounded clock on one would say the fan-out is the long
// part — so the dispatchers are asserted bounded in the same breath.
func TestTheTwoPassesDeclaredWithNoWallClockStillResolveToNegativeOne(t *testing.T) {
	for _, kind := range []string{"embed_reindex_workspace", "embed_drift_workspace"} {
		if got := declaredTimeout(t, kind, 0); got != -1 {
			t.Errorf("%s resolves to %v, want -1 — the pass is bounded by its backlog, and the row must stay outside River's rescuer", kind, got)
		}
	}
	for _, kind := range []string{"embed_reindex", "embed_drift_sweep"} {
		if got := declaredTimeout(t, kind, 0); got <= 0 {
			t.Errorf("%s resolves to %v, want a positive bound — a dispatcher only enumerates and enqueues", kind, got)
		}
	}
}

// TestTheDeepReadTimeoutIsTheOneSuppliedAtRegistration pins the single
// {operator: …} kind end to end: the file cannot state the value because the
// crawl wall is an operator's, so it declares who supplies it and the runner
// computes it. A policy that ignored the supplied value would silently swap
// a configured crawl's budget for a zero.
func TestTheDeepReadTimeoutIsTheOneSuppliedAtRegistration(t *testing.T) {
	caps := CrawlCaps{Wall: 30 * time.Minute}
	want := deepReadTimeout(caps)
	if got := declaredTimeout(t, "site_deep_read", want); got != want {
		t.Errorf("site_deep_read resolves to %v, want the %v computed from CrawlCaps at registration", got, want)
	}
}

// TestDeepReadTimeoutHoldsItsFloor covers the arithmetic the deleted method
// carried: a tightened crawl wall must not squeeze the terminal staging and
// dossier writes out of the budget.
func TestDeepReadTimeoutHoldsItsFloor(t *testing.T) {
	if got := deepReadTimeout(CrawlCaps{Wall: time.Second}); got != 8*time.Minute {
		t.Errorf("a one-second crawl wall yields %v, want the 8m floor", got)
	}
	long := deepReadTimeout(CrawlCaps{Wall: time.Hour})
	if want := time.Hour + extractLaneBudget + logoLaneBudget + time.Minute; long != want {
		t.Errorf("a one-hour crawl wall yields %v, want %v — every lane that can hold the job must be counted", long, want)
	}
}

// TestAddGovernedWorkerRecordsTheKindItRegistered is what makes MustBeTotal at
// boot mean anything: River keeps its own registry unexported, so a kind that
// went in without being recorded here would be invisible to the totality check
// and would run at whatever default it inherited.
func TestAddGovernedWorkerRecordsTheKindItRegistered(t *testing.T) {
	reg := newJobRegistry()
	addGovernedWorker[CloseDateSweepArgs](reg, &closeDateSweepWorker{}, 0)
	addGovernedWorker[CloseDateWorkspaceArgs](reg, &closeDateWorkspaceWorker{}, 0)

	want := []string{"close_date_sweep", "close_date_workspace"}
	if len(reg.kinds) != len(want) {
		t.Fatalf("recorded %v, want %v", reg.kinds, want)
	}
	for i, kind := range want {
		if reg.kinds[i] != kind {
			t.Errorf("recorded kind %d = %q, want %q", i, reg.kinds[i], kind)
		}
	}
	if err := jobs.MustBeTotal(reg.kinds); err != nil {
		t.Errorf("the kinds the runner registers must all be declared: %v", err)
	}
}
