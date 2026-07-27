// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The transport half of the admin user-map surface (crm.yaml's
// /overlay/user-map, /overlay/user-map/{id} and /overlay/owners). It carries
// no policy of its own: every one of these operations is admin-only,
// human-only and overlay-mode-only at the service entry point
// (usermapservice.go), so this file only decodes the request, hands it over,
// and maps the domain shapes onto the wire.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListOverlayUserMap answers the workspace's users with their incumbent-user
// mapping, and for an unmapped user the derived reason they have none.
func (h Handlers) ListOverlayUserMap(w http.ResponseWriter, r *http.Request, params crmcontracts.ListOverlayUserMapParams) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "listOverlayUserMap")
		return
	}
	cursor := ""
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	// A zero limit is "unspecified"; the store applies its own default and cap,
	// so this never invents a page size the contract did not ask for.
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	page, err := h.svc.UserMap(r.Context(), cursor, limit)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, userMapPageToWire(page))
}

// SetOverlayUserMap pins one user to an incumbent user as a manual admin
// override.
func (h Handlers) SetOverlayUserMap(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "setOverlayUserMap")
		return
	}
	var req crmcontracts.SetOverlayUserMapRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// incumbent_user_id is a required field (crm.yaml): refuse its absence here
	// rather than letting a blank owner reference reach the store, where it
	// would map the user onto every mirrored record that has no owner.
	if req.IncumbentUserId == "" {
		httperr.Write(w, r, httperr.Validation("incumbent_user_id", "required",
			"name the incumbent user this person maps to"))
		return
	}
	if err := h.svc.SetUserMap(r.Context(), ids.UserID{UUID: ids.UUID(id)}, req.IncumbentUserId); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteOverlayUserMap unmaps one user, revoking their mirror visibility, and
// records the decision so the reconcile sweep cannot re-create the mapping.
func (h Handlers) DeleteOverlayUserMap(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "deleteOverlayUserMap")
		return
	}
	if err := h.svc.UnmapUser(r.Context(), ids.UserID{UUID: ids.UUID(id)}); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListOverlayOwners answers the connected incumbent's user directory — the
// population the mapping picker chooses from.
func (h Handlers) ListOverlayOwners(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httperr.NotImplemented(w, r, "listOverlayOwners")
		return
	}
	dir, err := h.svc.Owners(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, ownerDirectoryToWire(dir))
}

// userMapPageToWire maps the domain UserMapPage onto the contract's
// OverlayUserMapPage. Entries is always a present (possibly empty) array — the
// contract declares it required, and a nil slice would serialize as null,
// which a client cannot iterate.
func userMapPageToWire(page UserMapPage) crmcontracts.OverlayUserMapPage {
	entries := make([]crmcontracts.OverlayUserMapEntry, 0, len(page.Entries))
	for _, v := range page.Entries {
		entries = append(entries, userMapEntryToWire(v))
	}
	return crmcontracts.OverlayUserMapPage{
		Incumbent:  page.Incumbent,
		Entries:    entries,
		NextCursor: optionalString(page.NextCursor),
	}
}

// userMapEntryToWire maps one derived view onto the contract entry.
// match_source is omitted entirely for an unmapped user rather than sent as an
// empty string: the contract's enum has no empty member, and "" would read as
// a match source nothing produced.
func userMapEntryToWire(v UserMapView) crmcontracts.OverlayUserMapEntry {
	stale := v.StaleOwnerRef
	entry := crmcontracts.OverlayUserMapEntry{
		UserId:             crmcontracts.Id(v.AppUserID.UUID),
		Email:              v.Email,
		Name:               optionalString(v.Name),
		IncumbentUserId:    optionalString(v.IncumbentUserID),
		IncumbentUserEmail: optionalString(v.OwnerEmail),
		IncumbentUserName:  optionalString(v.OwnerName),
		UnmappedReason:     crmcontracts.OverlayUserMapEntryUnmappedReason(v.UnmappedReason),
		StaleOwnerRef:      &stale,
	}
	if v.IncumbentUserID != "" && v.MatchSource != "" {
		source := crmcontracts.OverlayUserMapEntryMatchSource(v.MatchSource)
		entry.MatchSource = &source
	}
	return entry
}

// ownerDirectoryToWire maps the domain OwnerDirectory onto the contract's
// OverlayOwnerDirectory. Truncated rides as declared so the picker can say the
// list is partial instead of implying the incumbent has this many users.
func ownerDirectoryToWire(dir OwnerDirectory) crmcontracts.OverlayOwnerDirectory {
	owners := make([]crmcontracts.OverlayOwner, 0, len(dir.Owners))
	for _, o := range dir.Owners {
		owners = append(owners, crmcontracts.OverlayOwner{
			IncumbentUserId: o.ExternalID,
			Email:           o.Email,
			Name:            optionalString(o.Name),
		})
	}
	return crmcontracts.OverlayOwnerDirectory{
		Incumbent: dir.Incumbent,
		Owners:    owners,
		Truncated: dir.Truncated,
	}
}

// optionalString renders an absent domain string as an omitted wire field
// rather than an empty one, so a client can tell "the incumbent reports no
// name" from "the name is the empty string".
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
