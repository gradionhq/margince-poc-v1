// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// The two ends of the resolve seam spell the verdicts identically.
//
// This is the one divergence in D1 that nothing would report at runtime: the
// tool compares the verdict string to decide whether an exact hit is `matched`,
// and a renamed constant on the ladder's side would silently make EVERY exact
// hit read as ambiguous — an answer that is still well-formed, still plausible,
// and wrong on every call. Neither module can import the other, so this is the
// only place the two can be held together.
func TestTheSurfaceAndTheResolverAgreeOnVerdicts(t *testing.T) {
	for _, pair := range []struct{ surface, ladder string }{
		{agents.ResolveVerdictExact, string(people.VerdictExact)},
		{agents.ResolveVerdictAmbiguous, string(people.VerdictAmbiguous)},
		{agents.ResolveVerdictNone, string(people.VerdictNone)},
	} {
		if pair.surface != pair.ladder {
			t.Errorf("the tool reads %q and the ladder answers %q", pair.surface, pair.ladder)
		}
	}
}

// The mode guard is why this tool is composed here rather than registered beside
// its ladder: the ladder reads the native person and organization tables, which
// hold none of an overlay workspace's records. `unresolved` is the one decision
// that tells a caller creating a record is safe, so the unguarded answer would
// turn the duplicate guard into a duplicate factory.
func TestAnOverlayWorkspaceIsRefusedRatherThanResolvedAgainstEmptyTables(t *testing.T) {
	reached := false
	guarded := nativeOnlyResolver(stubOverlayMode{overlay: true},
		func(context.Context, []agents.ResolveCandidate) ([]agents.ResolveOutcome, error) {
			reached = true
			return nil, nil
		})

	_, err := guarded(t.Context(), []agents.ResolveCandidate{{Kind: "person", Name: "Anna Weber"}})
	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Errorf("err = %v, want the declared unsupported-by-SoR refusal", err)
	}
	if reached {
		t.Error("the ladder ran for an overlay workspace, against tables holding none of its records")
	}
}

// A native workspace reaches the ladder, so the guard above is a guard and not
// an outage.
func TestANativeWorkspaceReachesTheResolver(t *testing.T) {
	reached := false
	guarded := nativeOnlyResolver(stubOverlayMode{},
		func(context.Context, []agents.ResolveCandidate) ([]agents.ResolveOutcome, error) {
			reached = true
			return []agents.ResolveOutcome{{Verdict: agents.ResolveVerdictNone}}, nil
		})

	out, err := guarded(t.Context(), []agents.ResolveCandidate{{Kind: "person"}})
	if err != nil {
		t.Fatalf("a native workspace was refused: %v", err)
	}
	if !reached || len(out) != 1 {
		t.Errorf("the ladder answered %d outcomes (reached=%v), want its own answer carried through", len(out), reached)
	}
}

// A mode that cannot be read REFUSES rather than defaulting to native. Guessing
// native for an overlay workspace is the silent-empty-answer failure this whole
// family of guards exists to prevent.
func TestAnUnresolvedModeRefusesRatherThanAssumingNative(t *testing.T) {
	guarded := nativeOnlyResolver(stubOverlayMode{err: errors.New("the mode row is unreadable")},
		func(context.Context, []agents.ResolveCandidate) ([]agents.ResolveOutcome, error) {
			t.Error("the ladder ran for a workspace whose mode is unknown")
			return nil, nil
		})

	if _, err := guarded(t.Context(), []agents.ResolveCandidate{{Kind: "person"}}); err == nil {
		t.Error("an unreadable mode was treated as native")
	}
}
