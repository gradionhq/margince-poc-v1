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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The seam carries the EXACT flag, and that is the one field a silent drop would
// change the answer over.
//
// `Exact` is what turns a single surviving match into `matched` rather than
// `ambiguous`. A translation that forgot it would compile, pass every people
// test and every agents test — and quietly downgrade every key hit on the
// surface to "a person decides", which reads as caution rather than as a bug.
// Neither module can see the other, so this is the only place the two ends meet.
func TestTheSeamCarriesWhetherAKeyOrASimilarityNamedTheRecord(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ref   people.ResolveRef
		exact bool
	}{
		{"an exact lane", people.ResolveRef{Kind: people.ResolvePerson, ID: ids.NewV7(), Exact: true, Confidence: 1}, true},
		{"a name similarity", people.ResolveRef{Kind: people.ResolvePerson, ID: ids.NewV7(), Confidence: 0.8}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOutcomesFor([]people.ResolveOutcome{{Refs: []people.ResolveRef{tc.ref}}})
			if len(got) != 1 || len(got[0].Refs) != 1 {
				t.Fatalf("the adapter answered %+v, want the one ref carried through", got)
			}
			if got[0].Refs[0].Exact != tc.exact {
				t.Errorf("Exact = %v, want %v — the decision word is computed from this",
					got[0].Refs[0].Exact, tc.exact)
			}
			if got[0].Refs[0].ID != tc.ref.ID || got[0].Refs[0].Confidence != tc.ref.Confidence {
				t.Errorf("ref = %+v, want the ladder's own id and score", got[0].Refs[0])
			}
		})
	}
}

// The mode guard is why this tool is composed here rather than registered beside
// its ladder: the ladder reads the native person and organization tables, which
// hold none of an overlay workspace's records. `unresolved` is the answer that
// leaves a caller free to create, so the unguarded call would turn the duplicate
// guard into a duplicate factory.
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
			return []agents.ResolveOutcome{{}}, nil
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
