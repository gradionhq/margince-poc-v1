// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
)

// A staging's version pin is what stops a confirmed action executing against a
// row that changed underneath it. Declining the pin is a waiver, so it is held
// to the same discipline as the confirm-first one: every entry says why, and
// an entry naming a kind nothing stages is removed rather than left to rot.

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
	registered := map[string]bool{}
	for _, kind := range approvalsServiceWithEffects(nil).EffectKinds() {
		registered[kind] = true
	}
	for kind := range approvals.ContextTargetKinds() {
		if !registered[kind] {
			t.Errorf("contextTargetKinds[%s] names no registered effect kind — stale waiver, remove it", kind)
		}
	}
}
