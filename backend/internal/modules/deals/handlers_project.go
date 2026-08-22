// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The project transport: wire concerns only — decode, map to a store
// input, map the store's typed errors onto the codes the contract names.

import (
	"errors"
	"net/http"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListProjects serves the cursor-paginated project list.
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

// CreateProject opens a body of work on a company.
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

// GetProject serves one project, archived rows included.
func (h Handlers) GetProject(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	project, err := h.store.GetProject(r.Context(), pathID[ids.ProjectKind](id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, project)
}

// UpdateProject applies a partial update behind If-Match. Phase is not
// settable here — see AdvanceProjectPhase.
func (h Handlers) UpdateProject(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateProjectParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateProjectRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	in, err := projectUpdateInput(req, ifVersion)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}

	project, err := h.store.UpdateProject(r.Context(), pathID[ids.ProjectKind](id), in)
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

// TransferProjectOwnership is the bulk owner handover. The store answers a
// count and nothing else, so the wire carries a count and nothing else: a
// caller is never told which of the from-owner's projects were outside
// their write authority.
func (h Handlers) TransferProjectOwnership(w http.ResponseWriter, r *http.Request, _ crmcontracts.TransferProjectOwnershipParams) {
	var req crmcontracts.TransferProjectOwnershipRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in, err := projectTransferInput(req)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	moved, err := h.store.TransferProjectOwnership(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.TransferProjectOwnershipResult{Transferred: moved})
}

// projectTransferInput maps the handover body onto the store input. Both ids
// are required: an omitted one decodes to the zero UUID, which would read as
// "no such user" instead of "you named none".
func projectTransferInput(req crmcontracts.TransferProjectOwnershipRequest) (TransferProjectOwnershipInput, error) {
	if err := requireBodyID("from_owner_id", req.FromOwnerId); err != nil {
		return TransferProjectOwnershipInput{}, err
	}
	if err := requireBodyID("to_owner_id", req.ToOwnerId); err != nil {
		return TransferProjectOwnershipInput{}, err
	}
	return TransferProjectOwnershipInput{
		FromOwnerID: ids.From[ids.UserKind](ids.UUID(req.FromOwnerId)),
		ToOwnerID:   ids.From[ids.UserKind](ids.UUID(req.ToOwnerId)),
	}, nil
}

// ArchiveProject ends the grouping without ending what it grouped.
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
	name, err := projectName(req.Name)
	if err != nil {
		return CreateProjectInput{}, err
	}
	// A project belongs to a company, and the contract makes organization_id
	// required — but an absent key decodes to the zero UUID, which reaches
	// EnsureLinkTarget and comes back as a bare not-found naming no argument.
	if err := requireBodyID("organization_id", req.OrganizationId); err != nil {
		return CreateProjectInput{}, err
	}
	in := CreateProjectInput{
		Name:           name,
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

// projectName is the rule both write paths carry: a name of spaces is not a
// name. It satisfies "required" on the wire and then reads as blank on every
// screen that shows it, and the column is a bare NOT NULL, so nothing below
// this catches it. Stored trimmed, so what was accepted is what is shown.
func projectName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", &RequiredFieldError{Field: dealNameColumn}
	}
	return name, nil
}

func projectUpdateInput(req crmcontracts.UpdateProjectRequest, ifVersion *int64) (UpdateProjectInput, error) {
	// A PATCH that omits the name leaves it alone; one that sends a name is
	// held to the same rule create is, or the refusal would depend on which
	// verb happened to write the blank.
	if req.Name != nil {
		name, err := projectName(*req.Name)
		if err != nil {
			return UpdateProjectInput{}, err
		}
		req.Name = &name
	}
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
	return in, nil
}

// writeProjectErr maps the project store's typed errors onto the codes
// the contract names; false means none matched and writeStoreErr should
// keep falling through.
func writeProjectErr(w http.ResponseWriter, r *http.Request, err error) bool {
	var keyTaken *ProjectKeyTakenError
	if errors.As(err, &keyTaken) {
		// The existing id rides the 409 so a caller that collided can open
		// the project it collided with instead of hunting for it — but only
		// when that caller can see the row. A key held by a project outside
		// their scope still refuses the write and names nothing, because the
		// id would be the one thing the scope exists to withhold.
		existing := ""
		if keyTaken.ExistingID != nil {
			existing = keyTaken.ExistingID.String()
		}
		httperr.Write(w, r, httperr.Duplicate("project_key_taken", existing))
		return true
	}
	return false
}
