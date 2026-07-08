// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListLeads(w http.ResponseWriter, r *http.Request, params crmcontracts.ListLeadsParams) {
	in := ListLeadsInput{
		Cursor:          params.Cursor,
		Limit:           params.Limit,
		Query:           params.Q,
		IncludeArchived: params.IncludeArchived != nil && *params.IncludeArchived,
	}
	if params.Status != nil {
		s := string(*params.Status)
		in.Status = &s
	}
	in.OwnerID = idArg[ids.UserKind](params.OwnerId)

	leads, page, err := h.store.ListLeads(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.LeadListResponse{Data: leads, Page: pageInfo(page)})
}

func (h Handlers) CreateLead(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateLeadParams) {
	var req crmcontracts.CreateLeadRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	lead, created, err := h.store.CreateLead(r.Context(), leadCreateInput(req))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	status := http.StatusCreated
	if !created {
		// Natural-key replay: same (source_system, source_id) returns the
		// existing row, not a duplicate (features/01 §6.2).
		status = http.StatusOK
	}
	w.Header().Set("Location", "/v1/leads/"+lead.Id.String())
	httperr.WriteJSON(w, status, lead)
}

func (h Handlers) GetLead(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	lead, err := h.store.GetLead(r.Context(), pathID[ids.LeadKind](id), storekit.IncludeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, lead)
}

func (h Handlers) UpdateLead(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateLeadParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req LeadUpdateRequest
	if !httperr.Decode(w, r, &req) {
		return
	}

	lead, err := h.store.UpdateLead(r.Context(), pathID[ids.LeadKind](id), leadUpdateInput(req, ifVersion))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, lead)
}

// PromoteLead: POST /leads/{id}/promote — the lead graduates into the
// clean core on genuine engagement (features/01 §6.4). The 🟡
// agent-triggered path waits on the approvals machinery; today's callers
// are human sessions.
func (h Handlers) PromoteLead(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.PromoteLeadParams) {
	var req crmcontracts.PromoteLeadRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	// cold_outbound_no_reply is not in the enum BY DESIGN — an outbound
	// touch with no response never promotes (the anti-pollution line).
	if !req.Trigger.Valid() {
		httperr.Write(w, r, httperr.Validation("trigger", "trigger_not_allowed",
			"promotion needs genuine engagement: inbound_reply, meeting_booked, meeting_held or human_qualify"))
		return
	}

	in := PromoteLeadInput{Trigger: string(req.Trigger)}
	if req.Evidence != nil {
		in.EvidenceNote = req.Evidence.Note
		in.EvidenceActivityID = idArg[ids.ActivityKind](req.Evidence.ActivityId)
	}

	person, merged, err := h.store.PromoteLead(r.Context(), pathID[ids.LeadKind](id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	leadID := id
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.PromoteLeadResponse{
		LeadId: &leadID, Merged: merged, Person: person,
	})
}

// DisqualifyLead: DELETE /leads/{id} — the one path where
// "disqualified ⇒ archived" is enforced.
func (h Handlers) DisqualifyLead(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	lead, err := h.store.DisqualifyLead(r.Context(), pathID[ids.LeadKind](id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, lead)
}
