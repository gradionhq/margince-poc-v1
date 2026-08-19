// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) CreateTeam(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req crmcontracts.CreateTeamRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	team, err := h.svc.CreateTeam(r.Context(), actor, req.Name)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireAdminTeam(team))
}

func (h Handlers) UpdateTeam(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateTeamRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	team, err := h.svc.UpdateTeam(r.Context(), actor, ids.UUID(id), UpdateTeamInput{Name: req.Name, Archived: req.Archived})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireAdminTeam(team))
}

func (h Handlers) AddTeamMember(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, userID openapi_types.UUID) {
	h.setTeamMember(w, r, id, userID, true)
}

func (h Handlers) RemoveTeamMember(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, userID openapi_types.UUID) {
	h.setTeamMember(w, r, id, userID, false)
}

func (h Handlers) setTeamMember(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, userID openapi_types.UUID, on bool) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.svc.SetTeamMember(r.Context(), actor, ids.UUID(id), ids.UUID(userID), on); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handlers) PreviewAccess(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req crmcontracts.AccessPreviewRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	var teams []ids.UUID
	if req.TeamIds != nil {
		for _, t := range *req.TeamIds {
			teams = append(teams, ids.UUID(t))
		}
	}
	access, err := h.svc.PreviewAccess(r.Context(), actor, string(req.Role), teams)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireAccess(access))
}

func (h Handlers) GetUserAccess(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	access, err := h.svc.UserAccess(r.Context(), actor, ids.From[ids.UserKind](ids.UUID(id)))
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireAccess(access))
}

// wireAdminTeam maps a team the admin surface wrote; archived_at rides along
// because the admin list is the one place an archived team is still shown.
func wireAdminTeam(t Team) crmcontracts.Team {
	out := crmcontracts.Team{Id: openapi_types.UUID(t.ID), Name: t.Name}
	if t.ArchivedAt != nil {
		out.ArchivedAt = t.ArchivedAt
	}
	return out
}

// wireAccess maps the evaluated policy onto the contract. identity_read is
// the one fact the role document does not carry: customer identity is
// workspace-readable for every seat (platform/auth/tableclass.go).
func wireAccess(a Access) crmcontracts.AccessPreview {
	objects := make(map[string]struct {
		Create bool `json:"create"`
		Delete bool `json:"delete"`
		Read   bool `json:"read"`
		Update bool `json:"update"`
	}, len(a.Permissions.Objects))
	for object, g := range a.Permissions.Objects {
		objects[object] = struct {
			Create bool `json:"create"`
			Delete bool `json:"delete"`
			Read   bool `json:"read"`
			Update bool `json:"update"`
		}{Create: g.Create, Delete: g.Delete, Read: g.Read, Update: g.Update}
	}
	masks := make([]struct {
		Condition crmcontracts.AccessPreviewFieldMasksCondition `json:"condition"`
		Field     string                                        `json:"field"`
		Object    string                                        `json:"object"`
	}, 0, len(a.Permissions.FieldMasks))
	for _, m := range a.Permissions.FieldMasks {
		masks = append(masks, struct {
			Condition crmcontracts.AccessPreviewFieldMasksCondition `json:"condition"`
			Field     string                                        `json:"field"`
			Object    string                                        `json:"object"`
		}{Condition: crmcontracts.AccessPreviewFieldMasksCondition(m.Condition), Field: m.Field, Object: m.Object})
	}
	teams := make([]crmcontracts.Team, 0, len(a.Teams))
	for _, t := range a.Teams {
		teams = append(teams, wireTeam(t))
	}
	identityRead := crmcontracts.AccessPreviewIdentityReadWorkspace
	return crmcontracts.AccessPreview{
		Role:         a.Role,
		RowScope:     crmcontracts.AccessPreviewRowScope(a.Permissions.RowScope),
		IdentityRead: &identityRead,
		Objects:      objects,
		FieldMasks:   masks,
		Teams:        teams,
	}
}
