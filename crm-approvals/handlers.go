package crmapprovals

import (
	"encoding/json"
	"errors"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/fable-poc/crm-contracts"
	"github.com/gradionhq/fable-poc/internal/httperr"
	"github.com/gradionhq/fable-poc/kernel/ids"
)

// Handlers is the module's transport surface: the /approvals inbox ops.
type Handlers struct {
	svc *Service
}

func NewHandlers(svc *Service) Handlers { return Handlers{svc: svc} }

func (h Handlers) ListApprovals(w http.ResponseWriter, r *http.Request, params crmcontracts.ListApprovalsParams) {
	var status *string
	if params.Status != nil {
		s := string(*params.Status)
		status = &s
	}
	limit := 50
	if params.Limit != nil {
		limit = *params.Limit
	}
	rows, err := h.svc.List(r.Context(), status, limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	data := make([]crmcontracts.Approval, 0, len(rows))
	for _, a := range rows {
		data = append(data, wire(a))
	}
	writeJSON(w, http.StatusOK, crmcontracts.ApprovalListResponse{
		Data: data,
		Page: crmcontracts.PageInfo{HasMore: false},
	})
}

func (h Handlers) GetApproval(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	a, err := h.svc.Get(r.Context(), ids.UUID(id))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wire(a))
}

func (h Handlers) ApproveApproval(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.ApproveApprovalParams) {
	var req crmcontracts.ApproveRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
			return
		}
	}
	if req.EditedPayload != nil {
		writeErr(w, r, &EditNotSupportedError{})
		return
	}
	a, err := h.svc.Decide(r.Context(), ids.UUID(id), true, nil)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wire(a))
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
	a, err := h.svc.Decide(r.Context(), ids.UUID(id), false, req.Reason)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, wire(a))
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var decided *AlreadyDecidedError
	if errors.As(err, &decided) {
		httperr.Write(w, r, &httperr.DetailedError{
			Status: http.StatusConflict, Code: "already_decided", Detail: decided.Error(),
		})
		return
	}
	var edit *EditNotSupportedError
	if errors.As(err, &edit) {
		httperr.Write(w, r, httperr.Validation("edited_payload", "not_supported", edit.Error()))
		return
	}
	httperr.Write(w, r, err)
}

// wire maps the store row onto the contract shape; effectiveStatus folds
// lazy expiry in so a stale pending row never reads as approvable.
func wire(a row) crmcontracts.Approval {
	out := crmcontracts.Approval{
		Id:         openapi_types.UUID(a.ID),
		Kind:       a.Kind,
		Status:     crmcontracts.ApprovalStatus(a.effectiveStatus()),
		ProposedBy: a.ProposedBy,
		CreatedAt:  a.CreatedAt,
		DiffHash:   &a.DiffHash,
		Summary:    a.Summary,
		ExpiresAt:  &a.ExpiresAt,
		DecidedAt:  a.DecidedAt,
	}
	if a.OnBehalfOf != nil {
		v := openapi_types.UUID(*a.OnBehalfOf)
		out.OnBehalfOf = &v
	}
	if a.DecidedBy != nil {
		v := openapi_types.UUID(*a.DecidedBy)
		out.DecidedBy = &v
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
