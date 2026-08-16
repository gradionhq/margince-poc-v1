// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package pipelinetrace

import "testing"

// The registry's whole job is to refuse a stage that explains nothing. These
// gates are that refusal; without them the registry is a list, and a list is
// what three stages were already missing from.

func TestEveryStageIsRegistered(t *testing.T) {
	// The constants are the vocabulary; a constant with no registration is a
	// stage nobody gave an order, a subject or an answer source.
	all := []Stage{
		StageConnectorFilter, StageIngressGate, StageErasureCheck,
		StageInternalDrop, StageActivityWrite, StageTierLadder,
		StagePersonCreate, StageVerdict, StageCompanyTriage,
		StageAttentionLabel, StageMaterialEvents, StageClaimExtraction,
	}
	for _, stage := range all {
		if _, ok := Lookup(stage); !ok {
			t.Errorf("stage %q is declared but not registered", stage)
		}
	}
	if len(registrations) != len(all) {
		t.Errorf("registry holds %d stages, the vocabulary declares %d — a registration "+
			"exists for a stage that is not a declared constant", len(registrations), len(all))
	}
}

func TestEveryRegistrationExplainsItself(t *testing.T) {
	// A stage that reports nothing must say why, and it must say it as a catalog
	// key: verbatim English would render untranslated on a de or vi surface
	// while every other rung was localised.
	for _, r := range Registrations() {
		if len(r.Sources) == 0 {
			t.Errorf("%s declares no answer source", r.Stage)
			continue
		}
		answers := r.has(SourceStored) || r.has(SourceDerived)
		if !answers && r.AbsentReason == "" {
			t.Errorf("%s answers nothing and gives no reason — this is exactly the "+
				"silence the surface exists to remove", r.Stage)
		}
		if answers && r.AbsentReason != "" {
			t.Errorf("%s both answers and claims to be absent", r.Stage)
		}
		if r.has(SourcePlanned) && r.Issue == "" {
			t.Errorf("%s is planned with no issue ref, which makes the state an "+
				"excuse rather than a tracked debt", r.Stage)
		}
	}
}

func TestAnsweringStagesCarryReasons(t *testing.T) {
	// A skip without a reason is an absence with extra steps. Every stage that
	// can answer must close the set of reasons it may carry, so a value off the
	// wire can never be interpolated into a catalog key and rendered raw.
	for _, r := range Registrations() {
		if !r.has(SourceStored) && !r.has(SourceDerived) {
			continue
		}
		if len(r.Reasons) == 0 {
			t.Errorf("%s answers but closes no reason set", r.Stage)
		}
	}
}

func TestOrdersAreUniqueAndAscending(t *testing.T) {
	// The ladder is read top to bottom as the path a message took. Two stages
	// sharing an order makes that path non-deterministic between builds.
	seen := map[int]Stage{}
	for _, r := range registrations {
		if other, clash := seen[r.Order]; clash {
			t.Errorf("%s and %s share order %d", r.Stage, other, r.Order)
		}
		seen[r.Order] = r.Stage
	}
	ordered := Registrations()
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1].Order >= ordered[i].Order {
			t.Fatalf("Registrations() is not ascending at %d", i)
		}
	}
}

func TestOnlyStoredStagesReachTheStageColumn(t *testing.T) {
	// StoredStages() is what the migration's CHECK is asserted against. A
	// derived stage leaking into it would mean the column could hold a value no
	// writer ever produces.
	for _, stage := range StoredStages() {
		r, ok := Lookup(stage)
		if !ok {
			t.Fatalf("StoredStages returned unregistered %q", stage)
		}
		if !r.has(SourceStored) {
			t.Errorf("%s is not stored but appears in StoredStages()", stage)
		}
	}
	if got := len(StoredStages()); got != 3 {
		t.Errorf("StoredStages() = %d stages, want the 3 capture actually writes "+
			"(internal_drop, activity_write, tier_ladder); a change here needs a "+
			"migration changing the CHECK with it", got)
	}
}

func TestOnlyFunnelStagesCount(t *testing.T) {
	// The funnel metric is an in-process counter inside the trace writer, so a
	// stage that writes through it without belonging in the funnel would change
	// what an operator alerts on with no diff to any metric code. Opt-in is the
	// structural guard; this asserts it stays opt-in.
	if CountsInFunnel("a_stage_nobody_registered") {
		t.Error("an unregistered stage counts in the funnel")
	}
	for _, r := range Registrations() {
		if r.Funnel && !r.has(SourceStored) {
			t.Errorf("%s counts in the funnel but stores no rows to count", r.Stage)
		}
	}
}
