// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The blocked-domain surface: which domains this installation refuses a
// company, why, and what decided it — plus the admin's power to change any of
// it. Thin transport; the people store owns the RBAC gate, the normalization,
// the sticky-human rule and the re-ask that makes an unblock actually produce
// the company.

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// blockedDomainPageSize bounds the admin list. Large enough that a real
// installation's refusals fit on one screen-scroll, small enough that the query
// cannot become a table walk.
const blockedDomainPageSize = 200

type blockedDomainHandlers struct {
	people *people.Store
}

func (h blockedDomainHandlers) ListBlockedDomains(w http.ResponseWriter, r *http.Request) {
	// Human-only (x-agent-access): capture posture, not record data.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	entries, err := h.people.ListDomainAdmissions(r.Context(), blockedDomainPageSize)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	// Empty answers as [], never null — the contract promises an array.
	out := make([]crmcontracts.BlockedDomain, 0, len(entries))
	for _, e := range entries {
		out = append(out, toContractBlockedDomain(e))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.BlockedDomainListResponse{Data: out})
}

func (h blockedDomainHandlers) SetBlockedDomain(w http.ResponseWriter, r *http.Request) {
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	var body crmcontracts.SetBlockedDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "invalid_json", "request body is not valid JSON"))
		return
	}
	// The store answers with what it STORED, not what was sent: it normalizes
	// the domain to its registrable form and stamps the decision time itself.
	stored, err := h.people.SetDomainAdmission(r.Context(), body.Domain, string(body.Admission), body.Reason)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, toContractBlockedDomain(stored))
}

func toContractBlockedDomain(e people.BlockedDomain) crmcontracts.BlockedDomain {
	out := crmcontracts.BlockedDomain{
		Domain:    e.Domain,
		Admission: crmcontracts.BlockedDomainAdmission(e.Admission),
		Reason:    e.Reason,
		Source:    crmcontracts.BlockedDomainSource(e.Source),
		DecidedAt: e.DecidedAt,
	}
	if e.OrganizationID != nil {
		id := openapi_types.UUID(e.OrganizationID.UUID)
		out.OrganizationId = &id
	}
	return out
}
