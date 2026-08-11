// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// restoreComposed empties the composed table after a test. It is process-wide
// (a boot binding), so a test that left a kind behind would declare it into
// every suite that ran after it.
func restoreComposed(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if err := RegisterComposed(nil); err != nil {
			t.Errorf("restoring the composed table: %v", err)
		}
	})
}

func extSpec(kind string) Spec {
	return Spec{
		Kind:        kind,
		GoType:      "extJobWorkspaceArgs",
		Role:        Workspace,
		Queue:       "default",
		Timeout:     TimeoutPolicy{Fixed: time.Minute},
		MaxAttempts: 3,
		OptsOwner:   OptsFanOut,
	}
}

// TestComposedKindsAnswerSpecForLikeCoreOnes is the whole point of the seam: a
// composed kind is as DECLARED as a compiled one, so the wall clock Govern
// hands River, the queue a child lands on and the attempt cap all come from a
// declaration rather than from River's defaults.
func TestComposedKindsAnswerSpecForLikeCoreOnes(t *testing.T) {
	restoreComposed(t)
	if _, ok := SpecFor("ext_demo_refresh_ws"); ok {
		t.Fatal("a composed kind answered SpecFor before anything registered it")
	}
	if err := RegisterComposed([]Spec{extSpec("ext_demo_refresh_ws")}); err != nil {
		t.Fatalf("RegisterComposed: %v", err)
	}
	spec, ok := SpecFor("ext_demo_refresh_ws")
	if !ok {
		t.Fatal("SpecFor does not answer for a registered composed kind")
	}
	if spec.Timeout.Duration(0) != time.Minute || spec.MaxAttempts != 3 {
		t.Fatalf("composed spec came back as %+v", spec)
	}
	if err := MustBeTotal([]string{"ext_demo_refresh_ws"}); err != nil {
		t.Fatalf("MustBeTotal refused a declared composed kind: %v", err)
	}
	// And the compiled table is still answered — merging must not shadow core.
	if _, ok := SpecFor("close_date_sweep"); !ok {
		t.Fatal("a core kind stopped answering once a composed set was registered")
	}
}

// TestDeclaredWalksBothTablesInKindOrder: every consumer that builds a report
// or a metric family iterates Declared, so a composed kind has to appear there
// — and in the one deterministic order, not wherever a map put it.
func TestDeclaredWalksBothTablesInKindOrder(t *testing.T) {
	restoreComposed(t)
	if err := RegisterComposed([]Spec{extSpec("ext_demo_a_ws"), extSpec("ext_demo_b_ws")}); err != nil {
		t.Fatalf("RegisterComposed: %v", err)
	}
	var kinds []string
	for kind := range Declared() {
		kinds = append(kinds, kind)
	}
	if !slices.IsSorted(kinds) {
		t.Fatal("Declared did not walk in kind order")
	}
	for _, want := range []string{"ext_demo_a_ws", "ext_demo_b_ws", "close_date_sweep"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("Declared omitted %s", want)
		}
	}
}

// TestRegisterComposedRefusesWhatWouldMakeItAShadowingSurface: the seam exists
// to ADD declarations. One that could name a core kind would be a way to change
// a core job's wall clock from a directory an installation dropped in, and one
// that could name anything outside ext_ would not be an extension seam at all.
func TestRegisterComposedRefusesWhatWouldMakeItAShadowingSurface(t *testing.T) {
	restoreComposed(t)
	for _, tc := range []struct {
		name  string
		specs []Spec
		want  string
	}{
		{"outside the namespace", []Spec{extSpec("close_date_sweep_ws")}, "not an extension kind"},
		{"shadowing a core kind", []Spec{{Kind: "ext_demo_x"}, extSpec("close_date_sweep")}, "not an extension kind"},
		{"declared twice", []Spec{extSpec("ext_demo_x"), extSpec("ext_demo_x")}, "declared twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := RegisterComposed(tc.specs)
			if err == nil {
				t.Fatalf("RegisterComposed accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RegisterComposed(%s) = %v, want a message mentioning %q", tc.name, err, tc.want)
			}
			// Validate-then-apply: a refused set registers NONE of itself.
			if got := ComposedKinds(); len(got) != 0 {
				t.Fatalf("a refused registration left %v in the table", got)
			}
		})
	}
}

// TestRegisterComposedRefusesACoreKindByName covers the arm the namespace check
// cannot reach: a kind that IS in the ext_ namespace and IS already compiled
// would be a redefinition. No such core kind exists today, so the arm is
// exercised by asking the table directly.
func TestRegisterComposedRefusesACoreKindByName(t *testing.T) {
	restoreComposed(t)
	for kind := range specs {
		if IsExtensionKind(kind) {
			if err := RegisterComposed([]Spec{extSpec(kind)}); err == nil {
				t.Fatalf("RegisterComposed redefined the compiled kind %s", kind)
			}
			return
		}
	}
	// Nothing compiled sits in the extension namespace, which is itself the
	// property worth holding: api/jobs.yaml must never declare an ext_ kind, or
	// the two tables would be competing for one namespace.
	for kind := range specs {
		if strings.HasPrefix(kind, "ext_") {
			t.Fatalf("api/jobs.yaml declares %s in the extension namespace", kind)
		}
	}
}

// TestIsExtensionKindAsksTheNAMESPACENotTheTable: the vanilla build scraping a
// composed database is exactly the case the metric split exists for, and there
// the composed table is empty.
func TestIsExtensionKindAsksTheNAMESPACENotTheTable(t *testing.T) {
	restoreComposed(t)
	if !IsExtensionKind("ext_absent_unit_job_ws") {
		t.Error("an ext_ kind this build does not compose was not recognised as an extension kind")
	}
	if IsExtensionKind("close_date_sweep") {
		t.Error("a core kind was classified as an extension kind")
	}
	if got := ComposedKinds(); len(got) != 0 {
		t.Fatalf("ComposedKinds on a vanilla table: got %v, want none", got)
	}
}

// TestComposedFanOutUnitsCarryTheirEdge: a child's unit is declared on the
// DISPATCHER, and every consumer walks that edge backwards — so a composed
// dispatcher's edge has to be in the walk too.
func TestComposedFanOutUnitsCarryTheirEdge(t *testing.T) {
	restoreComposed(t)
	dispatcher := Spec{
		Kind:       "ext_demo_refresh",
		GoType:     "extJobDispatcherArgs",
		Role:       Dispatcher,
		Queue:      "default",
		Timeout:    TimeoutPolicy{Fixed: time.Minute},
		OptsOwner:  OptsArgs,
		FanOutUnit: FanOutWorkspace,
		FanOutTo:   "ext_demo_refresh_ws",
	}
	if err := RegisterComposed([]Spec{dispatcher, extSpec("ext_demo_refresh_ws")}); err != nil {
		t.Fatalf("RegisterComposed: %v", err)
	}
	if got := FanOutUnits()["ext_demo_refresh_ws"]; got != FanOutWorkspace {
		t.Fatalf("composed child's fan-out unit: got %v, want workspace", got)
	}
}
