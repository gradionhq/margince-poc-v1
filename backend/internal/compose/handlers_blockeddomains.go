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
	"fmt"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/freemail"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// blockedDomainPageSize bounds the admin list. Refusals accumulate on their own
// from every bulk-sender verdict, so the page is a page and the response says
// how many decisions exist — an operator hunting a missing company must be able
// to tell "not refused" from "past the end of this list".
const blockedDomainPageSize = 200

// maxBlockedDomainReason mirrors the contract's maxLength for the reason field.
const maxBlockedDomainReason = 500

type blockedDomainHandlers struct {
	people *people.Store
}

func (h blockedDomainHandlers) ListBlockedDomains(w http.ResponseWriter, r *http.Request) {
	// Human-only (x-agent-access): capture posture, not record data.
	if err := auth.RequireHuman(r.Context()); err != nil {
		httperr.Write(w, r, err)
		return
	}
	entries, total, err := h.people.ListDomainAdmissions(r.Context(), blockedDomainPageSize)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	// Empty answers as [], never null — the contract promises an array.
	out := make([]crmcontracts.BlockedDomain, 0, len(entries))
	for _, e := range entries {
		out = append(out, toContractBlockedDomain(e))
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.BlockedDomainListResponse{Data: out, Total: total})
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
	// Shape is the transport's job, and it is checked HERE so a caller learns
	// which field is wrong. The store re-checks both — it is reachable from
	// other callers — but its errors are internal ones, and a 500 for a missing
	// reason tells an admin nothing they can act on.
	// The generated type carries the enum check and never runs it, so an
	// unknown value would reach the store, come back as an internal error, and
	// answer 500 where the contract declares 422.
	if !body.Admission.Valid() {
		httperr.Write(w, r, httperr.Validation("admission", "invalid",
			`admission is either "suppressed" or "admitted"`))
		return
	}
	if strings.TrimSpace(body.Reason) == "" {
		httperr.Write(w, r, httperr.Validation("reason", "required",
			"a blocked domain needs a reason somebody can review"))
		return
	}
	// The contract says maxLength: 500 and the generated type does not enforce
	// it. Unchecked, one caller stores a megabyte per domain and every reader
	// of the list is served it back in full.
	if len([]rune(body.Reason)) > maxBlockedDomainReason {
		httperr.Write(w, r, httperr.Validation("reason", "too_long",
			fmt.Sprintf("a reason is at most %d characters; this one is %d",
				maxBlockedDomainReason, len([]rune(body.Reason)))))
		return
	}
	if _, ok := freemail.Hostname(body.Domain); !ok {
		httperr.Write(w, r, httperr.Validation("domain", "invalid",
			"expected a domain name like example.com; a full email address or a URL is not one"))
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
