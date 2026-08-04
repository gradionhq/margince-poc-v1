// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"slices"
	"testing"
)

// TestEveryFanOutUnitNamesAnArgsKey — ArgsKey is what the sweep-unit gauges
// group on, and a unit answering the empty string silently drops every kind
// declared with it out of the count. The set is closed and small enough to
// state, which is the point: a fourth unit added to the enum without a key
// fails here rather than at 3am on a dashboard reading zero.
func TestEveryFanOutUnitNamesAnArgsKey(t *testing.T) {
	keys := map[string]FanOutUnit{}
	for _, unit := range []FanOutUnit{FanOutWorkspace, FanOutConnection, FanOutBuild} {
		key := unit.ArgsKey()
		if key == "" {
			t.Errorf("fan-out unit %d names no args key, so a kind declared with it would be grouped on nothing", unit)
			continue
		}
		if other, taken := keys[key]; taken {
			t.Errorf("units %d and %d both name %q; two grains grouped on one key report one number for both", other, unit, key)
		}
		keys[key] = unit
	}
	// The zero unit is a kind that fans out to nothing, and it must answer no
	// key at all rather than default to the workspace's — a default would make
	// a missing declaration read as a declared one.
	if key := FanOutUnit(0).ArgsKey(); key != "" {
		t.Errorf("the zero fan-out unit answers %q; a kind that fans out to nothing has no unit to name", key)
	}
}

// TestFanOutUnitsReadsTheEdgeBackwards — the unit is declared on the
// DISPATCHER, so every consumer holding a child row needs the edge walked for
// it. A map built the other way round (child → its own zero unit) would answer
// plausibly and wrongly for every kind.
func TestFanOutUnitsReadsTheEdgeBackwards(t *testing.T) {
	units := FanOutUnits()
	if len(units) == 0 {
		t.Fatal("no fan-out edges at all — every check below would pass by iterating nothing")
	}
	for _, spec := range specs {
		if spec.FanOutTo == "" {
			if _, present := units[spec.Kind]; present && spec.Role == Dispatcher {
				t.Errorf("%s fans out to nothing but is answered as a fan-out child", spec.Kind)
			}
			continue
		}
		got, present := units[spec.FanOutTo]
		if !present {
			t.Errorf("%s declares it fans out to %s, which is not answered as a fan-out child", spec.Kind, spec.FanOutTo)
			continue
		}
		if got != spec.FanOutUnit {
			t.Errorf("%s stands for unit %d, want %d — the dispatcher %s declares it",
				spec.FanOutTo, got, spec.FanOutUnit, spec.Kind)
		}
	}
}

// TestSubWorkspaceFanOutsSelectsExactlyTheFinerGrains is the partition the two
// sweep pairs rest on: a kind read by BOTH would publish one number twice, and
// an operator summing the pairs would double-count a fleet. The selection is
// what enforces it — a finer-unit child's rows carry workspace_id as well, so
// nothing about the data would stop it appearing in both.
func TestSubWorkspaceFanOutsSelectsExactlyTheFinerGrains(t *testing.T) {
	kinds, argsKeys := subWorkspaceFanOuts()
	if len(kinds) != len(argsKeys) {
		t.Fatalf("%d kinds against %d args keys; the arrays are joined by index and would pair a kind with another's key",
			len(kinds), len(argsKeys))
	}
	if !slices.IsSorted(kinds) {
		t.Errorf("the kinds are unsorted (%v); a scrape target's series order must not flap between reads", kinds)
	}

	var workspaceGrain, finerGrain int
	for kind, unit := range FanOutUnits() {
		if unit == FanOutWorkspace {
			workspaceGrain++
			if slices.Contains(kinds, kind) {
				t.Errorf("%s fans out per workspace but is read by the unit pair, where it would restate margince_sweep_workspaces_total exactly", kind)
			}
			continue
		}
		finerGrain++
		i := slices.Index(kinds, kind)
		if i < 0 {
			t.Errorf("%s fans out per unit %d but the unit pair does not read it, so a failed unit stays masked by a healthy sibling", kind, unit)
			continue
		}
		if argsKeys[i] != unit.ArgsKey() {
			t.Errorf("%s is grouped on %q, want its declared unit's key %q", kind, argsKeys[i], unit.ArgsKey())
		}
	}
	if workspaceGrain == 0 || finerGrain == 0 {
		t.Fatalf("%d workspace-grain / %d finer-grain fan-outs declared; one side is unexercised and the partition is untested",
			workspaceGrain, finerGrain)
	}
}
