// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// What this writer archives, and what it does with a version it cannot honour.
//
// Both answers are reachable without a mirror store or an incumbent, and that
// is the point: they are decisions this provider makes about the REQUEST,
// before it goes anywhere near a row.

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// A version pin is REFUSED, not ignored.
//
// overlay_mirror carries an UpdatedAtBaseline and no `version` column, so there
// is no number here for a native version to be compared against. Accepting the
// pin and dropping it would tell a caller their precondition held when nothing
// ever read it — and on the agent door that caller is a human's released
// approval, granted against a version this write would not check.
func TestAnOverlayArchiveRefusesAVersionItCannotHonour(t *testing.T) {
	pin := int64(4)
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}

	_, err := (&Provider{}).ArchiveAt(context.Background(),
		datasource.ArchiveInput{Ref: ref, IfVersion: &pin})

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("archiving an overlay person at version 4 answered %v, want the unsupported-by-SoR "+
			"refusal — a precondition this seam cannot evaluate must not read as one that held", err)
	}
}

// The refusal is about the PIN, not about the seam being unavailable: the same
// call with no pin gets past this decision and on to the real work, which
// without a mirror store is the store's own complaint rather than this one.
func TestAnUnpinnedOverlayArchiveIsNotRefusedForThatReason(t *testing.T) {
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}

	_, err := (&Provider{}).ArchiveAt(context.Background(), datasource.ArchiveInput{Ref: ref})

	if errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("an unpinned overlay archive answered the unsupported-by-SoR refusal (%v) — that "+
			"answer belongs to a pin this seam cannot honour, and there was no pin", err)
	}
}

// What this writer archives is NARROWER than the native provider's set, and
// saying so is the whole reason the question is asked of the routed executor.
//
// Pinned as a set: a count agrees with a table that has swapped two members,
// and the refusal a model reads is built from these names.
func TestOverlayArchivesThreeOfTheNativeSix(t *testing.T) {
	types, err := (&Provider{}).ArchivableTypes(context.Background())
	if err != nil {
		t.Fatalf("asking overlay what it archives answered %v", err)
	}

	want := []datasource.EntityType{
		datasource.EntityDeal, datasource.EntityOrganization, datasource.EntityPerson,
	}
	if !slices.Equal(types, want) {
		t.Errorf("overlay archives %v, want %v — project, relationship and activity are archived by "+
			"the NATIVE provider and refused here, which is the disagreement a stage-time check that "+
			"read the native list could not see", types, want)
	}
}
