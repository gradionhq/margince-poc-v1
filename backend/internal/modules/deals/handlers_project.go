// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The project transport: wire concerns only — decode, map to a store
// input, map the store's typed errors onto the codes the contract names.

import (
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListProjects(w http.ResponseWriter, r *http.Request, params crmcontracts.ListProjectsParams) {
	in := ListProjectsInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		Query:           params.Q,
		Key:             params.Key,
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
		Sort:            params.Sort,
		CustomFilters:   httperr.CustomFieldFilters(r),
	}
	in.OrganizationID = idArg[ids.OrganizationKind](params.OrganizationId)
	in.OwnerID = idArg[ids.UserKind](params.OwnerId)
	if params.Phase != nil {
		phase := string(*params.Phase)
		in.Phase = &phase
	}

	projects, page, err := h.store.ListProjects(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.ProjectListResponse{Data: projects, Page: pageInfo(page)})
}

func (h Handlers) CreateProject(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateProjectParams) {
	var req crmcontracts.CreateProjectRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := projectCreateInput(req)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}

	project, err := h.store.CreateProject(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/projects/"+project.Id.String())
	httperr.WriteJSON(w, http.StatusCreated, project)
}

func (h Handlers) GetProject(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	project, err := h.store.GetProject(r.Context(), pathID[ids.ProjectKind](id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, project)
}

func (h Handlers) UpdateProject(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateProjectParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateProjectRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	project, err := h.store.UpdateProject(r.Context(), pathID[ids.ProjectKind](id), projectUpdateInput(req, ifVersion))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, project)
}

// AdvanceProjectPhase is the phase-move verb. It is a separate operation
// from update precisely so the transition, its history row and its
// first-class event are written together.
func (h Handlers) AdvanceProjectPhase(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.AdvanceProjectPhaseParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.AdvanceProjectPhaseRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	project, err := h.store.AdvanceProjectPhase(r.Context(), pathID[ids.ProjectKind](id), AdvanceProjectPhaseInput{
		ToPhase:   string(req.ToPhase),
		Reason:    req.Reason,
		IfVersion: ifVersion,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, project)
}

func (h Handlers) ArchiveProject(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ArchiveProjectParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	if _, err := h.store.ArchiveProject(r.Context(), pathID[ids.ProjectKind](id), ifVersion); err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func projectCreateInput(req crmcontracts.CreateProjectRequest) (CreateProjectInput, error) {
	if req.Name == "" {
		return CreateProjectInput{}, &RequiredFieldError{Field: "name"}
	}
	in := CreateProjectInput{
		Name:           req.Name,
		Key:            req.Key,
		OrganizationID: pathID[ids.OrganizationKind](req.OrganizationId),
		OwnerID:        idArg[ids.UserKind](req.OwnerId),
		Description:    req.Description,
		Source:         req.Source,
		CustomFields:   req.AdditionalProperties,
	}
	if req.StartedAt != nil {
		in.StartedAt = &req.StartedAt.Time
	}
	if req.TargetEndDate != nil {
		in.TargetEndDate = &req.TargetEndDate.Time
	}
	return in, nil
}

func projectUpdateInput(req crmcontracts.UpdateProjectRequest, ifVersion *int64) UpdateProjectInput {
	in := UpdateProjectInput{
		Name:         req.Name,
		Key:          req.Key,
		Description:  req.Description,
		OwnerID:      idArg[ids.UserKind](req.OwnerId),
		IfVersion:    ifVersion,
		CustomFields: req.AdditionalProperties,
	}
	if req.StartedAt != nil {
		in.StartedAt = &req.StartedAt.Time
	}
	if req.TargetEndDate != nil {
		in.TargetEndDate = &req.TargetEndDate.Time
	}
	if req.EndedAt != nil {
		in.EndedAt = &req.EndedAt.Time
	}
	return in
}

// writeProjectErr maps the project store's typed errors onto the codes
// the contract names; false means none matched and writeStoreErr should
// keep falling through.
func writeProjectErr(w http.ResponseWriter, r *http.Request, err error) bool {
	var keyTaken *ProjectKeyTakenError
	if errors.As(err, &keyTaken) {
		// The existing id rides the 409 so a caller that collided can open
		// the project it collided with instead of hunting for it. It is
		// absent only when the row was archived in the same instant.
		existing := ""
		if keyTaken.ExistingID != nil {
			existing = keyTaken.ExistingID.String()
		}
		httperr.Write(w, r, httperr.Duplicate("project_key_taken", existing))
		return true
	}
	var keyShape *ProjectKeyShapeError
	if errors.As(err, &keyShape) {
		httperr.Write(w, r, httperr.Validation("key", "invalid_key", keyShape.Error()))
		return true
	}
	var closedReason *ClosedReasonRequiredError
	if errors.As(err, &closedReason) {
		httperr.Write(w, r, httperr.Validation("reason", "closed_reason_required", closedReason.Error()))
		return true
	}
	var dateRange *ProjectDateRangeError
	if errors.As(err, &dateRange) {
		httperr.Write(w, r, httperr.Validation("ended_at", "invalid_date_range", dateRange.Error()))
		return true
	}
	var orgMismatch *DealProjectOrgMismatchError
	if errors.As(err, &orgMismatch) {
		httperr.Write(w, r, httperr.Validation("project_id", "project_organization_mismatch", orgMismatch.Error()))
		return true
	}
	var constraintErr *ProjectConstraintError
	if errors.As(err, &constraintErr) {
		httperr.Write(w, r, httperr.Validation(constraintErr.Constraint, "constraint_violated", constraintErr.Error()))
		return true
	}
	return false
}
