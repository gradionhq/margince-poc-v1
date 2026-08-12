// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The evaluator's two decisions that need no database: which actions the
// retain-only posture suppresses, and the pass bound the scheduler reads.

import (
	"testing"
	"time"
)

// TestIsDestructiveSpansTheClosedActionSet pins what the retain-only posture
// stops. `archive` is not destructive because archiving RETAINS — it stamps
// archived_at and the record stays readable, so a posture promising to keep
// everything has nothing to suppress there; `anonymize` and `erase` both
// destroy subject data and are exactly what it exists to stop.
func TestIsDestructiveSpansTheClosedActionSet(t *testing.T) {
	cases := map[string]bool{
		actionAnonymize: true,
		actionErase:     true,
		actionArchive:   false,
	}
	for action, want := range cases {
		if got := isDestructive(action); got != want {
			t.Errorf("isDestructive(%q) = %v, want %v", action, got, want)
		}
	}
	// An action the engine has no executor for is not treated as harmless: it
	// never reaches this question, because validateRetentionAction refuses it
	// first. Recorded here so the default branch is a decision, not an accident.
	if isDestructive("purge") {
		t.Error("isDestructive reported an unknown action as destructive; only the three known actions are classified")
	}
}

// TestMaxPassDurationTracksTheSelectorCount is the invariant #695 named as
// broken. The cap is one full batch of maximum-duration records per batched
// stage, and the stages are the authorable scopes plus the engine's own AI
// sweeps — so adding a selector must move the scheduler's cap with it. Derived
// from the same counts the engine uses on purpose: a hard-coded duration here
// would keep passing while the pass outran its window.
func TestMaxPassDurationTracksTheSelectorCount(t *testing.T) {
	perStage := retentionBatch * maxRecordDuration
	stages := len(retentionSelectors) + aiRetentionStages

	if want := time.Duration(stages) * perStage; MaxPassDuration != want {
		t.Fatalf("MaxPassDuration = %v, want %v (%d stages × %v)", MaxPassDuration, want, stages, perStage)
	}
	// Stated the other way round, which is the sentence the scheduler relies on:
	// the cap divides into exactly one batched stage per selector plus the AI
	// sweeps, with nothing left over.
	if MaxPassDuration%perStage != 0 {
		t.Errorf("MaxPassDuration %v is not a whole number of %v stages", MaxPassDuration, perStage)
	}
	if got := int(MaxPassDuration / perStage); got != stages {
		t.Errorf("MaxPassDuration allows %d batched stages, want %d — one per selector (%d) plus the AI sweeps (%d)",
			got, stages, len(retentionSelectors), aiRetentionStages)
	}
	// The authorable vocabulary and the selector table are the same list, so the
	// cap tracks what an admin can actually author.
	if len(AuthorableScopes())+aiRetentionStages != stages {
		t.Errorf("the cap counts %d stages but %d scopes are authorable plus %d AI sweeps",
			stages, len(AuthorableScopes()), aiRetentionStages)
	}
}
