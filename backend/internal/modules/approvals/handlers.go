// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Handlers is the module's transport surface: the /approvals inbox ops.
type Handlers struct {
	svc *Service
}

func NewHandlers(svc *Service) Handlers { return Handlers{svc: svc} }

// pathID asserts a contract path id as entity K's id — the widening
// point between the wire and the typed service surface (the route already
// names the entity, so the assertion lives here, not in the service).
func pathID[K ids.EntityKind](id crmcontracts.Id) ids.ID[K] {
	return ids.From[K](ids.UUID(id))
}

func (h Handlers) ListApprovals(w http.ResponseWriter, r *http.Request, params crmcontracts.ListApprovalsParams) {
	in, invalid := listInput(params)
	if invalid != nil {
		httperr.Write(w, r, invalid)
		return
	}
	rows, page, err := h.svc.List(r.Context(), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.Approval, 0, len(rows))
	for _, a := range rows {
		data = append(data, h.wire(a))
	}
	writeJSON(w, http.StatusOK, crmcontracts.ApprovalListResponse{
		Data: data,
		Page: pageInfo(page),
	})
}

// pageInfo puts the store's page on the wire. next_cursor is omitted rather
// than sent empty: an empty token is not a page a caller could ask for.
func pageInfo(p storekit.Page) crmcontracts.PageInfo {
	info := crmcontracts.PageInfo{HasMore: p.HasMore}
	if p.NextCursor != "" {
		info.NextCursor = &p.NextCursor
	}
	return info
}

// listInput binds the inbox query parameters, or answers the one validation
// error this surface has: the target pair is a discriminated reference, so
// half of it filters nothing a client could have meant — a type alone matches
// every record of that type, an id alone every type carrying that id.
func listInput(params crmcontracts.ListApprovalsParams) (ListInput, *httperr.DetailedError) {
	in := ListInput{Kind: params.Kind}
	if params.Status != nil {
		status := string(*params.Status)
		in.Status = &status
	}
	if params.Limit != nil {
		in.Limit = *params.Limit
	}
	if params.Cursor != nil {
		in.Cursor = *params.Cursor
	}
	if (params.TargetEntityType == nil) != (params.TargetEntityId == nil) {
		return ListInput{}, httperr.Validation("target_entity_type", "requires_pair",
			"filtering by target needs both target_entity_type and target_entity_id; supply both or neither")
	}
	if params.TargetEntityId != nil {
		targetID := ids.UUID(*params.TargetEntityId)
		in.TargetType, in.TargetID = params.TargetEntityType, &targetID
	}
	if params.BundleId != nil {
		bundleID := ids.UUID(*params.BundleId)
		in.BundleID = &bundleID
	}
	return in, nil
}

func (h Handlers) GetApproval(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	a, err := h.svc.Get(r.Context(), pathID[ids.ApprovalKind](id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.wire(a))
}

func (h Handlers) ApproveApproval(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ApproveApprovalParams) {
	var req crmcontracts.ApproveRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
			return
		}
	}
	approvalID := pathID[ids.ApprovalKind](id)
	var a row
	var err error
	if req.EditedPayload != nil {
		edited, marshalErr := json.Marshal(*req.EditedPayload)
		if marshalErr != nil {
			httperr.Write(w, r, httperr.Validation("edited_payload", "malformed_json", marshalErr.Error()))
			return
		}
		a, err = h.svc.DecideEdited(r.Context(), approvalID, edited)
	} else {
		a, err = h.svc.Decide(r.Context(), approvalID, true, nil)
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := h.wire(a)
	// The approve response carries the ADR-0036 compact JWS so a remote
	// redeemer can present a signed, effect-bound proof; the row remains
	// the single-use authority either way.
	token, err := h.svc.MintApprovalToken(r.Context(), approvalID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out.ApprovalToken = &token
	writeJSON(w, http.StatusOK, out)
}

func (h Handlers) RejectApproval(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req struct {
		Reason *string `json:"reason"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
			return
		}
	}
	a, err := h.svc.Decide(r.Context(), pathID[ids.ApprovalKind](id), false, req.Reason)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.wire(a))
}

// ApproveApprovalBundle decides every still-pending member of one bundle at
// once. Each member is decided on its own terms, so the response is per member.
func (h Handlers) ApproveApprovalBundle(w http.ResponseWriter, r *http.Request, bundleID crmcontracts.BundleId) {
	h.decideBundle(w, r, bundleID, true)
}

// RejectApprovalBundle is the rejecting half, on the same terms: a rejection is
// a decision, so it demands the same authority and is recorded the same way.
func (h Handlers) RejectApprovalBundle(w http.ResponseWriter, r *http.Request, bundleID crmcontracts.BundleId) {
	h.decideBundle(w, r, bundleID, false)
}

func (h Handlers) decideBundle(w http.ResponseWriter, r *http.Request, bundleID crmcontracts.BundleId, approve bool) {
	var req crmcontracts.ApprovalBundleDecisionRequest
	// httperr.Decode, not a bare json decode: this body has exactly one member,
	// and the field a client is most likely to send anyway is `edited_payload`
	// — an edit is a judgment about ONE proposed change and there is no such
	// thing as one spanning several. Accepting and ignoring it would approve the
	// agent's original payload while the human believed they had rewritten it.
	if r.Body != nil && r.ContentLength != 0 && !httperr.Decode(w, r, &req) {
		return
	}
	members, err := h.svc.DecideBundle(r.Context(), ids.UUID(bundleID), approve, req.Reason)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.ApprovalBundleMember, 0, len(members))
	for _, member := range members {
		data = append(data, crmcontracts.ApprovalBundleMember{
			Approval: h.wire(member.Approval),
			Outcome:  crmcontracts.ApprovalBundleMemberOutcome(member.Outcome),
		})
	}
	writeJSON(w, http.StatusOK, crmcontracts.ApprovalBundleDecision{BundleId: bundleID, Data: data})
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var oversized *BundleTooLargeError
	if errors.As(err, &oversized) {
		httperr.Write(w, r, httperr.Validation("bundle_id", "bundle_too_large", oversized.Error()))
		return
	}
	var decided *AlreadyDecidedError
	if errors.As(err, &decided) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict, Code: "already_decided", Detail: decided.Error(),
		})
		return
	}
	var retargeted *RetargetedEditError
	if errors.As(err, &retargeted) {
		httperr.Write(w, r, httperr.Validation("edited_payload", "retargeted", retargeted.Error()))
		return
	}
	var edit *InvalidEditError
	if errors.As(err, &edit) {
		httperr.Write(w, r, httperr.Validation("edited_payload", "malformed", edit.Error()))
		return
	}
	httperr.Write(w, r, err)
}

func (h Handlers) wire(a row) crmcontracts.Approval { return wire(a, h.svc.now()) }

// wire maps the store row onto the contract shape; effectiveStatus folds
// lazy expiry in so a stale pending row never reads as approvable.
func wire(a row, now time.Time) crmcontracts.Approval {
	out := crmcontracts.Approval{
		Id:         openapi_types.UUID(a.ID.UUID),
		Kind:       a.Kind,
		Status:     crmcontracts.ApprovalStatus(a.effectiveStatus(now)),
		ProposedBy: a.ProposedBy,
		CreatedAt:  a.CreatedAt,
		DiffHash:   &a.DiffHash,
		Summary:    a.Summary,
		ExpiresAt:  &a.ExpiresAt,
		DecidedAt:  a.DecidedAt,
	}
	if a.OnBehalfOf != nil {
		v := openapi_types.UUID(a.OnBehalfOf.UUID)
		out.OnBehalfOf = &v
	}
	if a.DecidedBy != nil {
		v := openapi_types.UUID(a.DecidedBy.UUID)
		out.DecidedBy = &v
	}
	if a.BundleID != nil {
		v := openapi_types.UUID(*a.BundleID)
		out.BundleId = &v
	}
	if a.TargetType != nil {
		out.TargetEntityType = a.TargetType
	}
	if a.TargetID != nil {
		v := openapi_types.UUID(*a.TargetID)
		out.TargetEntityId = &v
	}
	if len(a.ProposedChange) > 0 {
		var change map[string]any
		if json.Unmarshal(a.ProposedChange, &change) == nil {
			out.ProposedChange = &change
		}
	}
	return out
}

func writeJSON[T any](w http.ResponseWriter, status int, body T) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//craft:ignore swallowed-errors the status line is already written — a failed body encode has no channel left to report on
	_ = json.NewEncoder(w).Encode(body)
}
