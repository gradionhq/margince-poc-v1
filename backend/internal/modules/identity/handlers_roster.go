// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// ListUsers serves the workspace member roster. Cursor/Limit are accepted
// and ignored (the roster is small — mirrors ListRecordGrants); the page
// envelope is always empty.
func (h Handlers) ListUsers(w http.ResponseWriter, r *http.Request, params crmcontracts.ListUsersParams) {
	rows, err := h.svc.ListUsers(r.Context(), ListUsersInput{Q: params.Q})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.User, 0, len(rows))
	for _, u := range rows {
		data = append(data, wireUser(u))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.UserListResponse{Data: data, Page: crmcontracts.PageInfo{}})
}

// ListTeams serves the workspace teams with their active member count.
// Cursor/Limit are accepted and ignored, as in ListUsers.
func (h Handlers) ListTeams(w http.ResponseWriter, r *http.Request, params crmcontracts.ListTeamsParams) {
	rows, err := h.svc.ListTeams(r.Context(), ListTeamsInput{Q: params.Q})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.Team, 0, len(rows))
	for _, tm := range rows {
		data = append(data, wireTeam(tm))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.TeamListResponse{Data: data, Page: crmcontracts.PageInfo{}})
}

// wireUser maps a roster row to the contract User. workspace_id is
// required on User; no credential column ever leaves the store — userRow
// carries none, so none can leak here.
func wireUser(u userRow) crmcontracts.User {
	created := u.CreatedAt
	return crmcontracts.User{
		Id:          openapi_types.UUID(u.ID),
		WorkspaceId: openapi_types.UUID(u.WorkspaceID),
		Email:       openapi_types.Email(u.Email),
		DisplayName: u.DisplayName,
		Status:      crmcontracts.UserStatus(u.Status),
		IsAgent:     u.IsAgent,
		CreatedAt:   &created,
	}
}

// wireTeam maps a roster row to the contract Team, setting the optional
// member_count the roster read populates.
func wireTeam(tm teamRow) crmcontracts.Team {
	created := tm.CreatedAt
	count := tm.MemberCount
	return crmcontracts.Team{
		Id:          openapi_types.UUID(tm.ID),
		WorkspaceId: openapi_types.UUID(tm.WorkspaceID),
		Name:        tm.Name,
		MemberCount: &count,
		CreatedAt:   &created,
	}
}
