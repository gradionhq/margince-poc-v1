// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
)

// A staging's version pin is what stops a confirmed action executing against a
// row that changed underneath it. Declining the pin is a waiver, so both maps
// that decline it are held to the same discipline as the confirm-first one:
// every entry says why, an entry naming a kind nothing stages is removed rather
// than left to rot, and no kind may claim both reasons at once.

func TestEveryContextTargetKindIsExplained(t *testing.T) {
	declared := approvals.ContextTargetKinds()
	if len(declared) == 0 {
		t.Fatal("no context targets declared — if the waiver is unused, delete the mechanism")
	}
	for kind, why := range declared {
		if len(strings.TrimSpace(why)) < 40 {
			t.Errorf("contextTargetKinds[%s] has no real rationale — a staging that declines "+
				"its version pin must say what its target is context FOR", kind)
		}
	}
}

func TestEveryContextTargetKindIsAKindWeStage(t *testing.T) {
	registered := registeredEffectKinds()
	for kind := range approvals.ContextTargetKinds() {
		if !registered[kind] {
			t.Errorf("contextTargetKinds[%s] names no registered effect kind — stale waiver, remove it", kind)
		}
	}
}

func TestEveryUnpinnedKindIsExplained(t *testing.T) {
	declared := approvals.UnpinnedKinds()
	if len(declared) == 0 {
		t.Fatal("no unpinned kinds declared — if the waiver is unused, delete the mechanism")
	}
	for kind, why := range declared {
		if len(strings.TrimSpace(why)) < 40 {
			t.Errorf("unpinnedKinds[%s] has no real rationale — a staging that declines its version "+
				"pin over the very row its effect writes must say what the pin would have protected", kind)
		}
	}
}

func TestEveryUnpinnedKindIsAKindWeStage(t *testing.T) {
	registered := registeredEffectKinds()
	for kind := range approvals.UnpinnedKinds() {
		if !registered[kind] {
			t.Errorf("unpinnedKinds[%s] names no registered effect kind — stale waiver, remove it", kind)
		}
	}
}

// The two waivers make opposite claims about the same target — one says it is
// context the proposal is merely ABOUT, the other that it is the operand the
// effect writes. A kind declared in both asserts both, and whichever a reader
// believes, the other is a rationale that reads true and is not.
func TestNoKindIsBothContextOnlyAndUnpinned(t *testing.T) {
	for kind := range approvals.UnpinnedKinds() {
		if approvals.TargetIsContextOnly(kind) {
			t.Errorf("%s is declared in BOTH contextTargetKinds and unpinnedKinds — its target is either "+
				"context the proposal is about or the row its effect writes, and the two rationales say "+
				"opposite things about what a pin would have bound", kind)
		}
	}
}

func registeredEffectKinds() map[string]bool {
	registered := map[string]bool{}
	for _, kind := range approvalsServiceWithEffects(nil).EffectKinds() {
		registered[kind] = true
	}
	return registered
}
