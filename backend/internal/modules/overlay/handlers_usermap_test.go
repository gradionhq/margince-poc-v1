// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// userMapActorCtx binds one principal of typ holding grant on
// overlay_connection. The service's admission gate runs before it touches the
// pool, so the deny-path tests below need no database — and a Service built
// with none would panic loudly rather than pass if a gate ever stopped running
// first.
func userMapActorCtx(typ principal.PrincipalType, grant principal.ObjectGrant) context.Context {
	user := ids.New[ids.UserKind]()
	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	return principal.WithActor(ctx, principal.Principal{
		Type: typ, ID: string(typ) + ":" + user.String(), UserID: user.UUID,
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects:  map[string]principal.ObjectGrant{overlayConnectionObject: grant},
			RowScope: principal.RowScopeAll,
		},
	})
}

// Every role holds overlay_connection READ so a rep can see whether overlay
// mode is live (identity/internal/policy). This surface carries every user's
// email and the incumbent's whole directory, which no non-admin sees today —
// so it gates on UPDATE, and a read-granted rep must be refused on the READS
// as well as the writes.
func TestUserMapSurfaceRefusesAReadGrantedNonAdmin(t *testing.T) {
	svc := NewService(nil, nil, nil)
	repCtx := userMapActorCtx(principal.PrincipalHuman, principal.ObjectGrant{Read: true})
	someUser := ids.New[ids.UserKind]()

	if _, err := svc.UserMap(repCtx, "", 50); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("listing the user map needs the update grant, got: %v", err)
	}
	if _, err := svc.Owners(repCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("listing owners needs the update grant, got: %v", err)
	}
	if err := svc.SetUserMap(repCtx, someUser, "owner-1"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("setting a mapping needs the update grant, got: %v", err)
	}
	if err := svc.UnmapUser(repCtx, someUser); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("unmapping needs the update grant, got: %v", err)
	}
}

// An admin-minted passport must not widen its own visibility, and the
// contract's x-agent-access gate only inspects mutating methods — so the READS
// have to reject an agent principal at runtime, with the update grant present.
func TestUserMapSurfaceRefusesAnAgentPrincipal(t *testing.T) {
	svc := NewService(nil, nil, nil)
	agentCtx := userMapActorCtx(principal.PrincipalAgent, principal.ObjectGrant{Read: true, Update: true})
	someUser := ids.New[ids.UserKind]()

	if _, err := svc.UserMap(agentCtx, "", 50); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("an agent must not read the user map, got: %v", err)
	}
	if _, err := svc.Owners(agentCtx); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("an agent must not read the owners directory, got: %v", err)
	}
	if err := svc.SetUserMap(agentCtx, someUser, "owner-1"); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("an agent must not pin a mapping, got: %v", err)
	}
	if err := svc.UnmapUser(agentCtx, someUser); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Errorf("an agent must not unmap a user, got: %v", err)
	}
}

// entry builds one listed user for the derivation tests.
func entry(email, incumbentUserID, matchSource string, blocked bool) UserMapEntry {
	return UserMapEntry{
		AppUserID: ids.New[ids.UserKind](), Email: email, Name: "Listed User",
		IncumbentUserID: incumbentUserID, MatchSource: matchSource, Blocked: blocked,
	}
}

// The whole point of the surface is telling an admin WHY a user sees nothing,
// so every reason the contract enumerates has to be derivable from the same
// inputs the sweep decides on. Table-driven over one directory: the reasons
// differ only by the listed user, never by fixture shape.
func TestUnmappedReasonIsDerivedFromTheLiveDirectory(t *testing.T) {
	directory := []OwnerRef{
		{ExternalID: "owner-1", Email: "solo@acme.test", Name: "Solo Owner"},
		// Two DISTINCT owners share this email — the ambiguity SeedUserMap
		// refuses to seed.
		{ExternalID: "owner-2", Email: "shared@acme.test"},
		{ExternalID: "owner-3", Email: "shared@acme.test"},
		// The same owner listed twice, as overlapping directory pages do. It is
		// one owner, so it must NOT read as ambiguous.
		{ExternalID: "owner-1", Email: "solo@acme.test", Name: "Solo Owner"},
	}

	for _, tc := range []struct {
		name  string
		given UserMapEntry
		want  string
	}{
		{"mapped", entry("solo@acme.test", "owner-1", "email", false), reasonNone},
		{"blocked by an admin", entry("solo@acme.test", "", "", true), reasonBlocked},
		{"ambiguous email", entry("shared@acme.test", "", "", false), reasonAmbiguous},
		{"matched but not yet seeded", entry("solo@acme.test", "", "", false), reasonNotYetSynced},
		{"no owner carries the email", entry("nobody@acme.test", "", "", false), reasonNoEmailMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			views := userMapViews([]UserMapEntry{tc.given}, directory, true)
			if len(views) != 1 {
				t.Fatalf("want one view, got %d", len(views))
			}
			if views[0].UnmappedReason != tc.want {
				t.Errorf("reason = %q, want %q", views[0].UnmappedReason, tc.want)
			}
		})
	}
}

// An admin's own decision is the fact they have to undo to change the outcome,
// so it outranks whatever the email matching would otherwise have reported.
func TestABlockedUserReportsTheBlockEvenWhenAnOwnerMatchesTheirEmail(t *testing.T) {
	views := userMapViews(
		[]UserMapEntry{entry("solo@acme.test", "", "", true)},
		[]OwnerRef{{ExternalID: "owner-1", Email: "solo@acme.test"}}, true)

	if views[0].UnmappedReason != reasonBlocked {
		t.Errorf("reason = %q, want %q — the admin's block outranks the email match", views[0].UnmappedReason, reasonBlocked)
	}
}

// Without the directory, "no owner carries this email" and "we could not look"
// are indistinguishable. Reporting the first would hand the admin a fabricated
// diagnosis they would then act on.
func TestAnUnreadableDirectoryReportsItselfRatherThanGuessingAReason(t *testing.T) {
	views := userMapViews([]UserMapEntry{
		entry("solo@acme.test", "", "", false),
		entry("blocked@acme.test", "", "", true),
	}, nil, false)

	for _, v := range views {
		if v.UnmappedReason != reasonNoDirectory {
			t.Errorf("%s reason = %q, want %q", v.Email, v.UnmappedReason, reasonNoDirectory)
		}
	}
}

// A mapping pointing at an owner the incumbent no longer lists is REPORTED,
// never auto-revoked (design.md §4.6 rule 4): the override is the human's
// decision. Flagging it requires a directory we actually read — an unreadable
// one must not make every mapping look stale.
func TestAStaleOwnerReferenceIsFlaggedOnlyWhenTheDirectoryWasRead(t *testing.T) {
	stale := entry("rep@acme.test", "owner-gone", "manual", false)
	directory := []OwnerRef{{ExternalID: "owner-1", Email: "solo@acme.test", Name: "Solo Owner"}}

	read := userMapViews([]UserMapEntry{stale}, directory, true)
	if !read[0].StaleOwnerRef {
		t.Error("a mapping onto an owner absent from the directory must be flagged stale")
	}
	if read[0].UnmappedReason != reasonNone {
		t.Errorf("a stale mapping is still a mapping: reason = %q, want %q", read[0].UnmappedReason, reasonNone)
	}

	unread := userMapViews([]UserMapEntry{stale}, nil, false)
	if unread[0].StaleOwnerRef {
		t.Error("an unread directory must not make every mapping look stale")
	}
}

// The picker needs a person, not an opaque incumbent id, so a mapped user
// carries the owner's name and email from the live directory.
func TestAMappedUserCarriesTheOwnersIdentityFromTheDirectory(t *testing.T) {
	views := userMapViews(
		[]UserMapEntry{entry("rep@acme.test", "owner-1", "manual", false)},
		[]OwnerRef{{ExternalID: "owner-1", Email: "ada@acme.test", Name: "Ada Lovelace"}}, true)

	if views[0].OwnerName != "Ada Lovelace" || views[0].OwnerEmail != "ada@acme.test" {
		t.Errorf("owner identity = %q/%q, want Ada Lovelace/ada@acme.test", views[0].OwnerName, views[0].OwnerEmail)
	}
}

// match_source has no empty member in the contract enum, so an unmapped user
// must OMIT the field rather than send "" — a value nothing produced. The
// entries array is likewise always present: the contract declares it required,
// and a nil slice would serialize as null, which a client cannot iterate.
func TestUserMapWireOmitsMatchSourceForAnUnmappedUserAndAlwaysSendsEntries(t *testing.T) {
	page := userMapPageToWire(UserMapPage{
		Incumbent: "hubspot",
		Entries: []UserMapView{
			{
				UserMapEntry: entry("rep@acme.test", "owner-1", "manual", false),
				OwnerName:    "Ada Lovelace", OwnerEmail: "ada@acme.test", UnmappedReason: reasonNone,
			},
			{UserMapEntry: entry("nobody@acme.test", "", "", false), UnmappedReason: reasonNoEmailMatch},
		},
	})

	if page.Entries[0].MatchSource == nil || *page.Entries[0].MatchSource != crmcontracts.OverlayUserMapEntryMatchSourceManual {
		t.Errorf("a mapped user must report its match source, got %v", page.Entries[0].MatchSource)
	}
	if page.Entries[1].MatchSource != nil {
		t.Errorf("an unmapped user must omit match_source, got %q", *page.Entries[1].MatchSource)
	}
	if page.NextCursor != nil {
		t.Errorf("an exhausted page must omit next_cursor, got %q", *page.NextCursor)
	}

	empty, err := json.Marshal(userMapPageToWire(UserMapPage{Incumbent: "hubspot"}))
	if err != nil {
		t.Fatalf("encoding an empty page: %v", err)
	}
	if !strings.Contains(string(empty), `"entries":[]`) {
		t.Errorf("an empty page must carry an empty array, got %s", empty)
	}
}

// The picker cannot tell a partial directory from a small one, so truncation
// rides the wire explicitly and an owner with no reported name omits it rather
// than rendering an empty label.
func TestOwnerDirectoryWireCarriesTruncationAndOmitsAnAbsentName(t *testing.T) {
	dir := ownerDirectoryToWire(OwnerDirectory{
		Incumbent: "hubspot", Truncated: true,
		Owners: []OwnerRef{
			{ExternalID: "owner-1", Email: "ada@acme.test", Name: "Ada Lovelace"},
			{ExternalID: "owner-2", Email: "nameless@acme.test"},
		},
	})

	if !dir.Truncated {
		t.Error("a capped directory must say so")
	}
	if dir.Owners[0].Name == nil || *dir.Owners[0].Name != "Ada Lovelace" {
		t.Errorf("owner-1 name = %v, want Ada Lovelace", dir.Owners[0].Name)
	}
	if dir.Owners[1].Name != nil {
		t.Errorf("an owner the incumbent reports no name for must omit it, got %q", *dir.Owners[1].Name)
	}
	if dir.Owners[0].IncumbentUserId != "owner-1" {
		t.Errorf("owner id = %q, want owner-1", dir.Owners[0].IncumbentUserId)
	}
}

// A blank owner reference would map the user onto every mirrored record that
// has no owner at all, so the transport refuses it as a field error instead of
// letting it reach the store.
func TestSetOverlayUserMapRefusesABlankIncumbentUserId(t *testing.T) {
	h := NewHandlers(NewService(nil, nil, nil))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/overlay/user-map/"+ids.NewV7().String(),
		strings.NewReader(`{"incumbent_user_id":""}`))

	h.SetOverlayUserMap(w, r, crmcontracts.Id(ids.NewV7()))

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d for a blank incumbent_user_id", w.Code, http.StatusUnprocessableEntity)
	}
}

// The zero-value-constructible posture handlers.go's own doc names: a Handlers
// built with no Service answers an explicit 501 on every user-map verb rather
// than nil-derefing.
func TestUserMapHandlersAreNotImplementedWithoutAService(t *testing.T) {
	h := NewHandlers(nil)
	id := crmcontracts.Id(ids.NewV7())

	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"listOverlayUserMap": func(w http.ResponseWriter, r *http.Request) {
			h.ListOverlayUserMap(w, r, crmcontracts.ListOverlayUserMapParams{})
		},
		"setOverlayUserMap":    func(w http.ResponseWriter, r *http.Request) { h.SetOverlayUserMap(w, r, id) },
		"deleteOverlayUserMap": func(w http.ResponseWriter, r *http.Request) { h.DeleteOverlayUserMap(w, r, id) },
		"listOverlayOwners":    h.ListOverlayOwners,
	} {
		w := httptest.NewRecorder()
		call(w, httptest.NewRequest(http.MethodGet, "/overlay/user-map", strings.NewReader(`{}`)))
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s status = %d, want %d with no Service wired", name, w.Code, http.StatusNotImplemented)
		}
	}
}
