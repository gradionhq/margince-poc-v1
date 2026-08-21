// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The archive's three questions all route by the SAME mode read.
//
// That is the property worth pinning rather than any one answer: an
// installation whose mode says overlay must not have "what is archivable" or
// "what would be refused" answered by the native provider while the write goes
// to the mirror. A caller that got two of three from the wrong executor would
// stage an archive the writer refuses — which is the failure this seam exists
// to remove, reintroduced one layer down.
//
// The native provider is nil throughout, exactly as its sibling suite leaves
// it: a question that wrongly routes native panics rather than quietly passing.

import (
	"context"
	"slices"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// In overlay mode the archivable set is the MIRROR's three, not the native six.
//
// This is #2016 at the routing layer. The tool's own fallback list names what
// native archives; asking the dispatcher is how an overlay installation learns
// that project, relationship and activity are refused here.
func TestArchivableTypesAnswersForTheRoutedMode(t *testing.T) {
	wsID := ids.NewV7()
	d, calls := cachedModeDispatcher(wsID, modeNative)
	ctx := principal.WithWorkspaceID(context.Background(), wsID)

	types, err := d.ArchivableTypes(ctx)
	if err != nil {
		t.Fatalf("asking the dispatcher what it archives answered %v", err)
	}

	want := []datasource.EntityType{
		datasource.EntityDeal, datasource.EntityOrganization, datasource.EntityPerson,
	}
	if !slices.Equal(types, want) {
		t.Errorf("the dispatcher archives %v, want the overlay set %v — answering the native six "+
			"here admits three types this workspace's writer refuses, and the refusal then arrives "+
			"after a human has released the approval", types, want)
	}
	if *calls == 0 {
		t.Error("the answer came from the cached mode: which types are archivable is a fact about " +
			"the mode this request runs in, and a stale entry answers for the wrong writer")
	}
}

// The stage-time refusal routes by the same read, and reaches the overlay
// provider rather than the nil native one.
//
// The assertion is that it reaches a provider AT ALL — the error is overlay's
// own (no mirror store is wired in this fixture), which is exactly the evidence
// wanted: a question that had routed native would have panicked on nil.
func TestRefuseArchiveRoutesByTheSameModeRead(t *testing.T) {
	wsID := ids.NewV7()
	d, calls := cachedModeDispatcher(wsID, modeNative)
	ctx := principal.WithWorkspaceID(context.Background(), wsID)
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}

	if err := d.RefuseArchive(ctx, ref); err == nil {
		t.Fatal("RefuseArchive answered nil: it never reached a provider, so it refused nothing and " +
			"a staging it should have stopped would proceed to a human")
	}
	if *calls == 0 {
		t.Error("RefuseArchive answered from the cached mode; the refusals it reports belong to the " +
			"writer that will actually run, so the mode must be re-read")
	}
}

// A type the ROUTED executor does not archive is refused by that executor, not
// by a list this layer holds.
//
// `project` is archivable natively and is not in overlay's set, so it is the
// one probe that tells the two apart — a dispatcher answering from the native
// vocabulary would admit it here.
func TestRefuseArchiveRefusesATypeTheMirrorDoesNotArchive(t *testing.T) {
	wsID := ids.NewV7()
	d, _ := cachedModeDispatcher(wsID, modeOverlay)
	ctx := principal.WithWorkspaceID(context.Background(), wsID)
	ref := datasource.EntityRef{Type: datasource.EntityProject, ID: ids.NewV7()}

	if err := d.RefuseArchive(ctx, ref); err == nil {
		t.Fatal("staging a project archive against an overlay workspace was not refused — overlay " +
			"archives person, organization and deal, so this approval could never be carried out")
	}
}
