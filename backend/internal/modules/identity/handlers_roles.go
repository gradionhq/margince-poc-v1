// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The role grant editor's transport (GET /roles, PATCH
// /roles/{key}/objects/{object}). Thin, like every other admin handler here:
// the service owns the admin check, the vocabulary refusal and the audit; this
// file decides only what the two 404s are called on the wire and maps the row.

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// ListRoles (GET /roles): the editor's read.
func (h Handlers) ListRoles(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.ListRoles(r.Context(), actor)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	roles := make([]crmcontracts.Role, 0, len(rows))
	for _, row := range rows {
		roles = append(roles, wireRole(row))
	}
	// Admin-only, and the body is this workspace's permission model. A shared
	// proxy holding it would serve one workspace's grants to the next caller on
	// the same URL — the same reasoning as the roster's private cache header,
	// with a narrower audience.
	w.Header().Set("Cache-Control", "private, no-store")
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.RoleDirectory{Roles: roles})
}

// SetRoleObjectGrant (PATCH /roles/{key}/objects/{object}).
func (h Handlers) SetRoleObjectGrant(w http.ResponseWriter, r *http.Request, key, object string, _ crmcontracts.SetRoleObjectGrantParams) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	// The If-Match version is read off the header rather than the generated
	// params struct, which is the house spelling (quotas, people, deals):
	// httperr.IfMatchVersion is where "a bare integer, not a quoted ETag" and
	// the malformed-header refusal are decided once.
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.SetRoleObjectGrantRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	row, err := h.svc.SetRoleObjectGrant(r.Context(), actor, key, object, storedGrant{
		Create: req.Create, Read: req.Read, Update: req.Update, Delete: req.Delete,
	}, ifVersion)
	if err != nil {
		httperr.Write(w, r, unknownObjectRefusal(unknownRoleRefusal(err)))
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireRole(row))
}

// unknownObjectRefusal names the second of this surface's two 404s. Both mean
// "no such thing here" and a client that has to tell an admin WHICH thing needs
// the code — the same distinction unknownRoleRefusal draws, and the reason both
// are applied in sequence rather than one branch guessing.
//
// The detail points at the two places a legal object comes from rather than
// reciting them: the core set is closed but long, and the extension half is a
// property of this installation's composed units, which no fixed text can know.
func unknownObjectRefusal(err error) error {
	return refuseAs(err, errUnknownObject, http.StatusNotFound, "unknown_object",
		"this installation defines no RBAC object with that name; it must be one of the core "+
			"objects, or an ext_<unit>_<object> declared by an extension this build composed "+
			"(see the extensions inventory)")
}

// wireRole maps a role row onto the contract shape. The grant map is rebuilt
// entry by entry rather than type-asserted: the stored shape and the wire shape
// are the same four booleans today and are free to diverge, and a conversion
// the compiler checks is what keeps that safe.
//
// An empty document maps to an empty map, never nil — `null` on the wire reads
// as "unknown", and what is actually known is that this role grants nothing.
func wireRole(row roleRow) crmcontracts.Role {
	objects := make(map[string]crmcontracts.RbacObjectGrant, len(row.Objects))
	for object, g := range row.Objects {
		objects[object] = crmcontracts.RbacObjectGrant{
			Create: g.Create, Read: g.Read, Update: g.Update, Delete: g.Delete,
		}
	}
	return crmcontracts.Role{
		Key:      row.Key,
		Name:     row.Name,
		IsSystem: row.IsSystem,
		Version:  row.Version,
		Objects:  objects,
	}
}
