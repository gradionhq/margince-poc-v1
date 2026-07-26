// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The project-stakeholder surface. It lives here, not with the project
// record, because `relationship` is this module's table: a project
// stakeholder is the deal-stakeholder edge pointed at a body of work
// instead of a deal, and reusing the edge is what makes "which projects
// is this person accountable for" a query rather than a note.

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// projectStakeholderKind is the edge kind this surface reads and writes;
// projectObjectName is the RBAC object and visibility-probe table the edge
// annotates. Both spelled once so the kind, the anchor and the probe cannot
// drift apart.
const (
	projectStakeholderKind = "project_stakeholder"
	projectObjectName      = "project"
)

// ListProjectStakeholders serves the project-scoped stakeholder view; the
// project itself must be visible (the endpoint-scope rule then re-applies
// per edge).
func (h Handlers) ListProjectStakeholders(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	projectID := pathID[ids.ProjectKind](id)
	kind := projectStakeholderKind
	rels, page, err := h.store.ListRelationships(r.Context(), ListRelationshipsInput{
		Kind:      &kind,
		ProjectID: &projectID,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if len(rels) == 0 {
		// Distinguish "no stakeholders" from "no such project" without
		// leaking: the project read carries its own row scope.
		if err := h.store.EnsureProjectVisible(r.Context(), projectID); err != nil {
			writeStoreErr(w, r, err)
			return
		}
	}
	data := make([]crmcontracts.Relationship, 0, len(rels))
	for _, rel := range rels {
		data = append(data, wireRelationship(rel))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.RelationshipListResponse{Data: data, Page: pageInfo(page)})
}

// SetProjectStakeholder attaches a person to a project with a role. It is
// a PUT because it is idempotent per person: re-stating an existing edge
// updates its role rather than raising a duplicate, which is what a caller
// correcting a role actually means.
func (h Handlers) SetProjectStakeholder(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.SetProjectStakeholderParams) {
	var req crmcontracts.SetProjectStakeholderRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	role := string(req.Role)
	rel, err := h.store.SetProjectStakeholder(r.Context(), SetProjectStakeholderInput{
		ProjectID: pathID[ids.ProjectKind](id),
		PersonID:  pathID[ids.PersonKind](req.PersonId),
		Role:      role,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wireRelationship(rel))
}

// RemoveProjectStakeholder detaches a person from a project by archiving
// the edge — detaching is not deleting.
func (h Handlers) RemoveProjectStakeholder(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, personID openapi_types.UUID, _ crmcontracts.RemoveProjectStakeholderParams) {
	err := h.store.RemoveProjectStakeholder(r.Context(),
		pathID[ids.ProjectKind](id), ids.From[ids.PersonKind](ids.UUID(personID)))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
