// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// ListUsers serves one keyset page of the workspace member roster.
func (h Handlers) ListUsers(w http.ResponseWriter, r *http.Request, params crmcontracts.ListUsersParams) {
	rows, page, err := h.svc.ListUsers(r.Context(), ListUsersInput{
		Q:      params.Q,
		Cursor: params.Cursor,
		Limit:  params.Limit,
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.User, 0, len(rows))
	for _, u := range rows {
		data = append(data, wireUser(u))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.UserListResponse{Data: data, Page: pageInfo(page)})
}

// ListTeams serves one keyset page of the workspace teams with their
// active member count.
func (h Handlers) ListTeams(w http.ResponseWriter, r *http.Request, params crmcontracts.ListTeamsParams) {
	rows, page, err := h.svc.ListTeams(r.Context(), ListTeamsInput{
		Q:      params.Q,
		Cursor: params.Cursor,
		Limit:  params.Limit,
	})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.Team, 0, len(rows))
	for _, tm := range rows {
		data = append(data, wireTeam(tm))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.TeamListResponse{Data: data, Page: pageInfo(page)})
}

// pageInfo renders the store's keyset page onto the contract's PageInfo
// envelope — this module's own copy of the one-per-module spelling
// (people/deals/activities/signals/quotas each carry their own).
func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
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
