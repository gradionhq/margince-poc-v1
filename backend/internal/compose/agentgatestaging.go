// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The 🟡 loop on the REST door: how a confirm-first call is staged for a human,
// and how the retry that carries their decision is redeemed.
//
// Split from agentgate.go on the 500-line cap, along the boundary that was
// already there: agentgate.go decides whether a call is ADMITTED, and this is
// what happens to the one answer that is neither yes nor no. The MCP door's
// twin of this file is modules/agents/approvals.go, and the two must agree on
// what "the identical call" means — which is why both hash through
// shared/kernel/diffhash rather than each spelling it.

import (
	"fmt"
	"net/http"
	"strconv"

	chi "github.com/go-chi/chi/v5"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// stageOrRedeem handles the 🟡 outcome. The identical call is the
// redemption key — a content hash over operation + concrete path +
// canonicalized body, computed the same way at staging and at retry: an
// X-Approval-Token redeems a previously approved identical call and lets
// it through; otherwise the call is staged as a new approval and refused
// with the redemption instructions.
func stageOrRedeem(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, pol agentPolicy, body []byte) {
	if redeemIfPresented(w, r, next, staging, pol, body) {
		return
	}
	stageRefusal(w, r, staging, pol, body)
}

// redeemIfPresented consumes an X-Approval-Token when the request carries
// one: a valid token bound to this exact call lets it through to the
// handler; an invalid one is answered with the failure — asserted
// authority is validated, never ignored. Reports whether the request was
// fully handled (no token → false, the caller continues its own flow).
func redeemIfPresented(w http.ResponseWriter, r *http.Request, next http.Handler, staging agents.Approvals, pol agentPolicy, body []byte) bool {
	token := r.Header.Get(approvalTokenHeader)
	if token == "" {
		return false
	}
	approvalID, pErr := ids.ParseAs[ids.ApprovalKind](token)
	if pErr != nil {
		httperr.Write(w, r, fmt.Errorf("agent gate: malformed %s: %w", approvalTokenHeader, apperrors.ErrApprovalTokenInvalid))
		return true
	}
	_, diffHash, cErr := canonicalRESTCall(pol.Op, r.URL.Path, body)
	if cErr != nil {
		httperr.Write(w, r, cErr)
		return true
	}
	if staging == nil {
		httperr.Write(w, r, fmt.Errorf("agent gate: %s presented but this surface has no approvals engine: %w",
			approvalTokenHeader, apperrors.ErrApprovalTokenInvalid))
		return true
	}
	// Redeeming and marking are one step (agents.RedeemAndMark), so this
	// transport cannot forward an approved call without the released marker the
	// seam's egress backstop reads — nor obtain that marker without redeeming.
	released, pin, pinned, rErr := agents.RedeemAndMark(r.Context(), staging, approvalID, pol.Tool, diffHash)
	if rErr != nil {
		httperr.Write(w, r, rErr)
		return true
	}
	// Redemption commits its OWN transaction, and the handler below opens a
	// fresh one to write. The skew check inside the redemption therefore
	// proves the row was at the pinned version when the approval was
	// consumed, not that it still is when the effect lands — and the attacker
	// controls both sides of that window, since the redeeming request and any
	// racing auto-execute mutation come from the same agent. Carrying the pin
	// forward as the request's own If-Match makes the store re-check it
	// inside the transaction that actually mutates, where a concurrent write
	// loses to the version compare instead of to timing.
	if pinned {
		r.Header.Set("If-Match", strconv.FormatInt(pin, 10))
	}
	// WithContext shares the header map set just above, so the pin travels with
	// the released request.
	next.ServeHTTP(w, r.WithContext(released))
	return true
}

// stageRefusal stages the refused call as a pending approval and answers
// with the redemption instructions — the whole request, unapplied, is the
// staged change, so the approved retry is this exact request again.
func stageRefusal(w http.ResponseWriter, r *http.Request, staging agents.Approvals, pol agentPolicy, body []byte) {
	ctx := r.Context()
	canonical, diffHash, cErr := canonicalRESTCall(pol.Op, r.URL.Path, body)
	if cErr != nil {
		httperr.Write(w, r, cErr)
		return
	}
	// Stage only what a human can actually decide: a kind with no
	// decision-grant mapping would sit undecidable in every inbox
	// — refuse instead of minting a zombie authority object.
	if !approvals.KindHasDecisionGrants(pol.Tool) {
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s (%s) has no approval decision mapping: %w", pol.Op, pol.Tool, apperrors.ErrPermissionDenied))
		return
	}
	var targetID ids.UUID
	if raw := chi.URLParam(r, "id"); raw != "" {
		var err error
		if targetID, err = ids.Parse(raw); err != nil {
			httperr.Write(w, r, apperrors.ErrNotFound)
			return
		}
	}
	// A concrete target with no record type is unstageable authority: the
	// approvals surface scopes an inbox row by probing its target's own/team
	// visibility, and it cannot probe a type it was not told. Such a row
	// would show a record's summary and proposed change to everyone holding
	// the object grant, and let any of them decide a write against a row
	// their own scope hides. Refuse it here, the same fail-closed shape as
	// an undecidable kind, rather than mint an unscopable authority object.
	if targetID != (ids.UUID{}) && pol.RecordType == "" {
		httperr.Write(w, r, fmt.Errorf(
			"agent gate: %s stages against a concrete record but declares no record type: %w",
			pol.Op, apperrors.ErrPermissionDenied))
		return
	}
	// The version a human approves is pinned SERVER-SIDE, inside the staging
	// transaction, by approvals.StageInTx — the one place every stager passes
	// through, so the REST gate, the MCP tool twins and the automation engine
	// cannot each get it differently. The gate deliberately passes NO pin of
	// its own: the only one it could offer is the agent's own If-Match header,
	// which is optional, and an agent that simply omitted it staged
	// target_version NULL — a NULL the redemption skew check short-circuits
	// on. A create (no target id) has nothing to pin, and says so by carrying
	// a zero id.
	approvalID, sErr := staging.Stage(ctx, agents.StageRequest{
		Tool:           pol.Tool,
		ProposedChange: canonical,
		DiffHash:       diffHash,
		TargetType:     string(pol.RecordType),
		TargetID:       targetID,
		Summary:        restSummary(pol.Op, r.Method, r.URL.Path, body),
	})
	if sErr != nil {
		httperr.Write(w, r, sErr)
		return
	}
	httperr.Write(w, r, fmt.Errorf(
		"staged as approval %s — once a human approves it, repeat this exact request with the %s: %s header: %w",
		approvalID, approvalTokenHeader, approvalID, apperrors.ErrRequiresApproval))
}
