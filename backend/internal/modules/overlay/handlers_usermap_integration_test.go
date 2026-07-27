// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// The user-map transport driven end to end over a real service: the status
// codes and the JSON the settings card is built against. The wire MAPPING is
// unit-tested (handlers_usermap_test.go); what needs a database is proving the
// handlers actually reach the governed service and encode what it returns —
// a mapper test cannot tell a wired handler from a stubbed one.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// decodeJSON reads a recorded JSON response into out, failing loudly on a
// status other than want — a decode into the zero value would otherwise make a
// 404 body look like an empty page. Pass a FRESH out every time: decoding into
// a populated struct merges rather than replaces, so an omitted optional field
// would silently keep the previous response's value.
//
//craft:ignore naked-any the JSON deserialization seam: out is whichever generated contract response struct the assertion below reads
func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, want int, out any) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, want, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decoding the response: %v (body: %s)", err, rec.Body.String())
	}
}

// The whole surface in the order the settings card uses it: read the
// directory, pin a user to an owner from it, read the page back, then unmap.
func TestUserMapHandlersServeTheAdminRoundTrip(t *testing.T) {
	ctx, pool, ws := testWorkspaceCtx(t)
	svc := connectedUserMapService(ctx, t, pool, &directoryIncumbent{
		owners: []OwnerRef{{ExternalID: "owner-1", Email: "ada@acme.test", Name: "Ada Lovelace"}},
	})
	h := NewHandlers(svc)
	_, repRaw := testWorkspaceCtxAsUser(t, ws, "rep@acme.test")
	rep := ids.From[ids.UserKind](repRaw)

	rec := httptest.NewRecorder()
	h.ListOverlayOwners(rec, httptest.NewRequest(http.MethodGet, "/overlay/owners", nil).WithContext(ctx))
	var dir crmcontracts.OverlayOwnerDirectory
	decodeJSON(t, rec, http.StatusOK, &dir)
	if len(dir.Owners) != 1 || dir.Owners[0].IncumbentUserId != "owner-1" || dir.Truncated {
		t.Fatalf("owners directory = %+v, want the one seeded owner, untruncated", dir)
	}

	rec = httptest.NewRecorder()
	h.SetOverlayUserMap(rec,
		httptest.NewRequest(http.MethodPut, "/overlay/user-map/"+rep.String(),
			strings.NewReader(`{"incumbent_user_id":"owner-1"}`)).WithContext(ctx),
		crmcontracts.Id(rep.UUID))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ListOverlayUserMap(rec,
		httptest.NewRequest(http.MethodGet, "/overlay/user-map", nil).WithContext(ctx),
		crmcontracts.ListOverlayUserMapParams{})
	var page crmcontracts.OverlayUserMapPage
	decodeJSON(t, rec, http.StatusOK, &page)
	if page.Incumbent != "hubspot" {
		t.Errorf("page incumbent = %q, want hubspot", page.Incumbent)
	}
	mapped := wireEntryFor(t, page, rep)
	if mapped.IncumbentUserId == nil || *mapped.IncumbentUserId != "owner-1" {
		t.Errorf("mapped owner = %v, want owner-1", mapped.IncumbentUserId)
	}
	if mapped.MatchSource == nil || *mapped.MatchSource != crmcontracts.OverlayUserMapEntryMatchSourceManual {
		t.Errorf("match source = %v, want manual", mapped.MatchSource)
	}
	if mapped.IncumbentUserName == nil || *mapped.IncumbentUserName != "Ada Lovelace" {
		t.Errorf("owner name = %v, want Ada Lovelace", mapped.IncumbentUserName)
	}

	rec = httptest.NewRecorder()
	h.DeleteOverlayUserMap(rec,
		httptest.NewRequest(http.MethodDelete, "/overlay/user-map/"+rep.String(), nil).WithContext(ctx),
		crmcontracts.Id(rep.UUID))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want %d (body: %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ListOverlayUserMap(rec,
		httptest.NewRequest(http.MethodGet, "/overlay/user-map", nil).WithContext(ctx),
		crmcontracts.ListOverlayUserMapParams{})
	var afterUnmap crmcontracts.OverlayUserMapPage
	decodeJSON(t, rec, http.StatusOK, &afterUnmap)
	unmapped := wireEntryFor(t, afterUnmap, rep)
	if unmapped.IncumbentUserId != nil {
		t.Errorf("an unmapped user must carry no owner, got %q", *unmapped.IncumbentUserId)
	}
	// The block the unmap recorded is exactly why this user is now unmapped —
	// reporting no_email_match here would send the admin looking for a missing
	// owner that is sitting right there in the directory.
	if unmapped.UnmappedReason != crmcontracts.OverlayUserMapEntryUnmappedReasonBlockedByAdmin {
		t.Errorf("unmapped reason = %q, want blocked_by_admin", unmapped.UnmappedReason)
	}
}

// Native mode has no incumbent, so the read answers the /overlay cluster's
// mode_not_overlay 404 rather than an empty page a card would render as
// "nobody to map".
func TestListOverlayUserMapIs404InNativeMode(t *testing.T) {
	ctx, pool, _ := testWorkspaceCtx(t)
	h := NewHandlers(NewService(pool, keyvault.NewMemory(), NewMirrorStore(pool, noOwnerEmails{})))

	rec := httptest.NewRecorder()
	h.ListOverlayUserMap(rec,
		httptest.NewRequest(http.MethodGet, "/overlay/user-map", nil).WithContext(ctx),
		crmcontracts.ListOverlayUserMapParams{})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d in native mode (body: %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mode_not_overlay") {
		t.Errorf("body = %s, want the mode_not_overlay code", rec.Body.String())
	}
}

// wireEntryFor finds one user's row in a decoded page.
func wireEntryFor(t *testing.T, page crmcontracts.OverlayUserMapPage, user ids.UserID) crmcontracts.OverlayUserMapEntry {
	t.Helper()
	for _, e := range page.Entries {
		if ids.UUID(e.UserId) == user.UUID {
			return e
		}
	}
	t.Fatalf("user %s is missing from the page", user)
	return crmcontracts.OverlayUserMapEntry{}
}
