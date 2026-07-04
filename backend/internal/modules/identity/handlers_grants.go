package identity

import (
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListRecordGrants(w http.ResponseWriter, r *http.Request, params crmcontracts.ListRecordGrantsParams) {
	in := ListGrantsInput{}
	if params.RecordType != nil {
		v := string(*params.RecordType)
		in.RecordType = &v
	}
	if params.RecordId != nil {
		id := ids.UUID(*params.RecordId)
		in.RecordID = &id
	}
	if params.SubjectType != nil {
		v := string(*params.SubjectType)
		in.SubjectType = &v
	}
	if params.SubjectId != nil {
		id := ids.UUID(*params.SubjectId)
		in.SubjectID = &id
	}
	grants, err := h.svc.ListRecordGrants(r.Context(), in)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.RecordGrant, 0, len(grants))
	for _, g := range grants {
		data = append(data, wireGrant(g))
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"data": data, "page": crmcontracts.PageInfo{}})
}

func (h Handlers) CreateRecordGrant(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateRecordGrantParams) {
	var req crmcontracts.CreateRecordGrantRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	grant, err := h.svc.CreateRecordGrant(r.Context(), CreateGrantInput{
		RecordType:  string(req.RecordType),
		RecordID:    ids.UUID(req.RecordId),
		SubjectType: string(req.SubjectType),
		SubjectID:   ids.UUID(req.SubjectId),
		Access:      string(req.Access),
		Reason:      req.Reason,
		ExpiresAt:   req.ExpiresAt,
	})
	if err != nil {
		writeGrantErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wireGrant(grant))
}

func (h Handlers) RevokeRecordGrant(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.RevokeRecordGrantParams) {
	if err := h.svc.RevokeRecordGrant(r.Context(), ids.UUID(id)); err != nil {
		writeGrantErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeGrantErr(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *InvalidScopeError
	if errors.As(err, &invalid) {
		httperr.Write(w, r, httperr.Validation("record_type", "invalid", invalid.Error()))
		return
	}
	httperr.Write(w, r, err)
}
