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
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
// provider's own work rather than the nil native one.
//
// The context carries a real actor with the delete grant, and that is the whole
// design of this case rather than setup. Without one, overlay's RefuseArchive
// refuses at its `auth.Require` and the test passes on "no actor bound to
// context" — an error every path in the tree can raise, satisfying the
// assertion by a mechanism upstream of the subject. What is wanted is the
// refusal that proves the call got as far as overlay's OWN state: this fixture
// wires no mirror store, so reaching that is reaching errNoMirrorStore.
func TestRefuseArchiveRoutesByTheSameModeRead(t *testing.T) {
	wsID := ids.NewV7()
	d, calls := cachedModeDispatcher(wsID, modeNative)
	ctx := principal.WithActor(principal.WithWorkspaceID(context.Background(), wsID),
		principal.Principal{
			Type: principal.PrincipalHuman, ID: "archive-probe", SeatType: principal.SeatFull,
			UserID: ids.NewV7(),
			Permissions: principal.Permissions{
				Objects: map[string]principal.ObjectGrant{"person": {Delete: true}},
			},
		})
	ref := datasource.EntityRef{Type: datasource.EntityPerson, ID: ids.NewV7()}

	err := d.RefuseArchive(ctx, ref)

	if err == nil {
		t.Fatal("RefuseArchive answered nil: it never reached a provider, so it refused nothing and " +
			"a staging it should have stopped would proceed to a human")
	}
	// The refusal named in this case's own doc, not merely "not the upstream
	// one": an assertion that only excludes today's impostor admits tomorrow's.
	if !strings.Contains(err.Error(), "no mirror store") {
		t.Fatalf("RefuseArchive answered %v, want overlay's own no-mirror-store refusal — anything "+
			"else is a guard upstream of the routing this case exists to pin, and would hold "+
			"whether or not the routing works", err)
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
	// modeNative, so the cached answer DISAGREES with the workspace row. Seeded
	// as modeOverlay the two agree, and the case cannot observe which was read
	// — measured: swapping RefuseArchive to the cached read left it green.
	d, calls := cachedModeDispatcher(wsID, modeNative)
	ctx := principal.WithWorkspaceID(context.Background(), wsID)
	ref := datasource.EntityRef{Type: datasource.EntityProject, ID: ids.NewV7()}

	err := d.RefuseArchive(ctx, ref)

	// The SENTINEL, not merely an error. This context binds no actor, so an
	// assertion on "some error" is satisfied by overlay's object gate — a
	// refusal that has nothing to do with which types the mirror archives and
	// that would hold if the type check were deleted outright.
	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("staging a project archive against an overlay workspace answered %v, want the "+
			"unsupported-by-SoR refusal — overlay archives person, organization and deal, so this "+
			"approval could never be carried out", err)
	}
	if *calls == 0 {
		t.Error("the refusal came from the cached mode, which disagrees with the workspace row here " +
			"— the types a staging is judged against belong to the writer that will actually run")
	}
}
