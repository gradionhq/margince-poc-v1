// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The flip's pure decisions, unit-tested away from Postgres: the
// confirm-first request gate, the stage catalog's placement rules, the
// disclosure lines, and the advisory-lock key. Each of these is a
// judgement the integration lanes exercise only incidentally — a wrong
// answer here is a silently mis-imported estate, not a failing query.

import (
	"errors"
	"strings"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestParseFlipRequestIsConfirmFirst(t *testing.T) {
	emergency := crmcontracts.OverlayFlipRequestModeEmergency
	bogus := crmcontracts.OverlayFlipRequestMode("sideways")

	t.Run("the exact phrase defaults to a fresh-sync flip", func(t *testing.T) {
		mode, err := parseFlipRequest(crmcontracts.OverlayFlipRequest{ConfirmationPhrase: "FLIP TO SOR"})
		if err != nil {
			t.Fatalf("parseFlipRequest: %v", err)
		}
		if mode != crmcontracts.OverlayFlipRequestModeFreshSync {
			t.Errorf("mode = %q, want fresh_sync", mode)
		}
	})

	t.Run("an absent body carries no phrase and is refused", func(t *testing.T) {
		if _, err := parseFlipRequest(crmcontracts.OverlayFlipRequest{}); err == nil {
			t.Fatal("the zero request must be refused — it is what an empty body decodes to")
		}
	})

	t.Run("a near-miss phrase is refused", func(t *testing.T) {
		for _, phrase := range []string{"flip to sor", "FLIP TO SOR ", "FLIPTOSOR"} {
			if _, err := parseFlipRequest(crmcontracts.OverlayFlipRequest{ConfirmationPhrase: phrase}); err == nil {
				t.Errorf("phrase %q was accepted; only the exact phrase may arm the flip", phrase)
			}
		}
	})

	t.Run("emergency is honoured when asked for explicitly", func(t *testing.T) {
		mode, err := parseFlipRequest(crmcontracts.OverlayFlipRequest{
			ConfirmationPhrase: "FLIP TO SOR", Mode: &emergency,
		})
		if err != nil || mode != crmcontracts.OverlayFlipRequestModeEmergency {
			t.Fatalf("mode = %q, err = %v; want emergency", mode, err)
		}
	})

	t.Run("an unknown mode is refused rather than silently defaulted", func(t *testing.T) {
		if _, err := parseFlipRequest(crmcontracts.OverlayFlipRequest{
			ConfirmationPhrase: "FLIP TO SOR", Mode: &bogus,
		}); err == nil {
			t.Fatal("an unrecognised mode must not fall through to fresh_sync")
		}
	})
}

func TestFlipBlockedNamesEveryUnsatisfiedGate(t *testing.T) {
	err := flipBlocked([]crmcontracts.OverlayFlipPreflightBlocking{
		crmcontracts.IncumbentUnreachable, crmcontracts.ExportMissing,
	})
	if !errors.Is(err, apperrors.ErrOverlayFlipBlocked) {
		t.Fatalf("err = %v, want the ErrOverlayFlipBlocked identity (the 409 mapping reads it)", err)
	}
	for _, reason := range []string{"incumbent_unreachable", "export_missing"} {
		if !strings.Contains(err.Error(), reason) {
			t.Errorf("detail %q omits %s — the caller cannot see which gate to clear", err, reason)
		}
	}
}

func TestStalenessDisclosesAgeOrNothing(t *testing.T) {
	at, seconds := staleness(time.Time{})
	if at != nil || seconds != nil {
		t.Errorf("a never-synced mirror returned (%v, %v); a fabricated zero would read as freshly synced", at, seconds)
	}
	at, seconds = staleness(time.Now().Add(-90 * time.Second))
	if at == nil || seconds == nil {
		t.Fatal("a synced mirror must disclose its age")
	}
	if *seconds < 89 || *seconds > 120 {
		t.Errorf("staleness = %ds, want ~90", *seconds)
	}
}

func TestFlipLockKeyIsStablePerWorkspaceAndNonNegative(t *testing.T) {
	a, b := ids.NewV7(), ids.NewV7()
	// Derived from the id's bytes alone, so a workspace resolved through
	// a different route (parsed from its string form, say) still keys the
	// same lock — the claim and the liveness probe compute it separately.
	reparsed, err := ids.Parse(a.String())
	if err != nil {
		t.Fatalf("re-parsing the workspace id: %v", err)
	}
	if flipLockKey(a) != flipLockKey(reparsed) {
		t.Error("the same workspace produced two keys; the probe would stop seeing the claim")
	}
	if flipLockKey(a) == flipLockKey(b) {
		t.Error("distinct workspaces collided; one workspace's flip would block another's disconnect")
	}
	if flipLockKey(a) < 0 {
		t.Errorf("key %d is negative — the pg_locks classid/objid reconstruction assumes it is not", flipLockKey(a))
	}
}

func TestFlipImportableIsTheClosedEstateSet(t *testing.T) {
	for _, object := range flipImportOrder {
		if !flipImportable(object) {
			t.Errorf("%q is imported but not importable — the identity map's allowlist and the import order disagree", object)
		}
	}
	for _, object := range []string{"relationship", "pipeline", "workspace", "", "person; DROP TABLE person"} {
		if flipImportable(object) {
			t.Errorf("%q must not reach a lookup: the allowlist is what keeps the table name out of a format string", object)
		}
	}
}

func TestNormalizeStageKeyFoldsPresentation(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Closed Won", "closedwon"},
		{"closed-won", "closedwon"},
		{"  CLOSED_WON  ", "closedwon"},
		{"Appointment Scheduled", "appointmentscheduled"},
	} {
		if got := normalizeStageKey(tc.in); got != tc.want {
			t.Errorf("normalizeStageKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// stageCatalogFixture is a default pipeline with one open and both
// terminal stages, plus a second pipeline the estate may name.
func stageCatalogFixture() *flipStageCatalog {
	cat := &flipStageCatalog{
		openIn: map[ids.PipelineID]ids.StageID{},
		byName: map[string]flipStage{}, bySemantic: map[string]flipStage{},
	}
	defaultPipeline := ids.New[ids.PipelineKind]()
	other := ids.New[ids.PipelineKind]()
	cat.add(flipStage{id: ids.New[ids.StageKind](), pipeline: defaultPipeline, semantic: stageSemanticOpen}, "Discovery", true)
	cat.add(flipStage{id: ids.New[ids.StageKind](), pipeline: defaultPipeline, semantic: stageSemanticWon}, "Closed Won", true)
	cat.add(flipStage{id: ids.New[ids.StageKind](), pipeline: defaultPipeline, semantic: stageSemanticLost}, "Closed Lost", true)
	cat.add(flipStage{id: ids.New[ids.StageKind](), pipeline: other, semantic: stageSemanticOpen}, "Qualifying", false)
	return cat
}

func TestStageCatalogPlacement(t *testing.T) {
	cat := stageCatalogFixture()

	t.Run("a named open stage is matched directly", func(t *testing.T) {
		p := cat.place("Discovery")
		if !p.matched || p.closedStage != nil {
			t.Fatalf("placement = %+v, want a matched open stage", p)
		}
		if p.birthStage != cat.byName["discovery"].id {
			t.Error("the deal was not born on the stage its incumbent named")
		}
	})

	t.Run("a closed stage is born open then advanced", func(t *testing.T) {
		p := cat.place("Closed Won")
		if !p.matched || p.closedStage == nil || p.closedSemantic != stageSemanticWon {
			t.Fatalf("placement = %+v, want a won placement", p)
		}
		if p.birthStage != cat.firstOpen {
			t.Error("a closed deal must be born on an open stage — the store forbids any other birth")
		}
	})

	t.Run("the incumbent's canonical closed keys resolve by semantic", func(t *testing.T) {
		for key, want := range map[string]string{"closedwon": stageSemanticWon, "closedlost": stageSemanticLost} {
			p := cat.place(key)
			if !p.matched || p.closedSemantic != want {
				t.Errorf("place(%q) = %+v, want the %s stage", key, p, want)
			}
		}
	})

	t.Run("an unmatched stage falls back to the default pipeline, disclosed", func(t *testing.T) {
		p := cat.place("Negotiating Terms")
		if p.matched {
			t.Fatal("an unknown incumbent stage must not report as matched")
		}
		if p.pipeline != cat.pipeline || p.birthStage != cat.firstOpen {
			t.Errorf("placement = %+v, want the default pipeline's first open stage", p)
		}
		note := stageDisclosure(p, "Negotiating Terms", "d-1")
		if !strings.Contains(note, "d-1") || !strings.Contains(note, "Negotiating Terms") {
			t.Errorf("disclosure %q must name the deal and the stage it could not place", note)
		}
	})

	t.Run("a stageless incumbent deal is disclosed too", func(t *testing.T) {
		note := stageDisclosure(cat.place(""), "", "d-2")
		if !strings.Contains(note, "d-2") || !strings.Contains(note, "no incumbent stage") {
			t.Errorf("disclosure %q must say the deal named no stage at all", note)
		}
	})

	t.Run("a matched placement discloses nothing", func(t *testing.T) {
		if note := stageDisclosure(cat.place("Discovery"), "Discovery", "d-3"); note != "" {
			t.Errorf("a clean placement disclosed %q", note)
		}
	})
}

func TestAdoptedDealClosesOnlyWhenItIsStillOpen(t *testing.T) {
	closed := flipPlacement{closedStage: &[]ids.StageID{ids.New[ids.StageKind]()}[0], closedSemantic: stageSemanticWon}
	open := flipPlacement{}

	// The case the repair exists for: the crash created the deal on its
	// open birth stage and died before the close. Left alone it would be
	// reported converged while sitting open, and the estate's won revenue
	// would simply be missing.
	if !adoptedDealNeedsClosing(closed, deals.DealOpen) {
		t.Error("a deal the incumbent calls closed, still open natively, must be closed on the resumed attempt")
	}
	// The ordinary replay: the attempt that created it also closed it.
	// Advancing again would refight a settled close and its FX freeze.
	for _, status := range []deals.DealStatus{deals.DealWon, deals.DealLost} {
		if adoptedDealNeedsClosing(closed, status) {
			t.Errorf("a deal already %s was advanced again", status)
		}
	}
	// The incumbent says open, so open is correct and there is nothing
	// to assert — closing here would invent a terminal state.
	if adoptedDealNeedsClosing(open, deals.DealOpen) {
		t.Error("an open incumbent deal must not be closed")
	}
}
