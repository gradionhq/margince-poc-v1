// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The dedupe review-queue transport (DH-EXT-1/2): wire decode, store-error
// mapping, and the row→contract rendering — the evidence snapshot passes
// through verbatim (DH-N-8; the transport must not re-derive or reshape
// what the detector saw).

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListDedupeCandidates serves the confidence-sorted queue page (DH-EXT-1).
func (h Handlers) ListDedupeCandidates(w http.ResponseWriter, r *http.Request, params crmcontracts.ListDedupeCandidatesParams) {
	in := DedupeQueueInput{}
	if params.Status != nil {
		in.Status = string(*params.Status)
	}
	if params.EntityType != nil {
		in.EntityType = string(*params.EntityType)
	}
	if params.Cursor != nil {
		in.Cursor = *params.Cursor
	}
	if params.Limit != nil {
		in.Limit = *params.Limit
	}
	rows, next, err := h.store.ListDedupeCandidates(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	resp := crmcontracts.DedupeCandidateListResponse{Data: make([]crmcontracts.DedupeCandidate, 0, len(rows))}
	for _, row := range rows {
		c, err := toContractDedupeCandidate(row)
		if err != nil {
			writeStoreErr(w, r, err)
			return
		}
		resp.Data = append(resp.Data, c)
	}
	if next != "" {
		resp.Page = &crmcontracts.PageInfo{HasMore: true, NextCursor: &next}
	} else {
		resp.Page = &crmcontracts.PageInfo{HasMore: false}
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// GetDedupeCandidate serves one pair with its full evidence (DH-EXT-1).
func (h Handlers) GetDedupeCandidate(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	row, err := h.store.GetDedupeCandidate(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	h.writeDedupeCandidate(w, r, row)
}

// DisposeDedupeCandidate decides one pair (DH-EXT-2).
func (h Handlers) DisposeDedupeCandidate(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	var req crmcontracts.DedupeDispositionRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	var winner *ids.UUID
	if req.WinnerId != nil {
		u := ids.UUID(*req.WinnerId)
		winner = &u
	}
	row, err := h.store.DisposeDedupeCandidate(r.Context(), ids.UUID(id), string(req.Disposition), winner)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	h.writeDedupeCandidate(w, r, row)
}

// UndoDedupeDisposition re-opens a dismissed pair (DH-EXT-2).
func (h Handlers) UndoDedupeDisposition(w http.ResponseWriter, r *http.Request, id openapi_types.UUID) {
	row, err := h.store.UndoDedupeDisposition(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	h.writeDedupeCandidate(w, r, row)
}

func (h Handlers) writeDedupeCandidate(w http.ResponseWriter, r *http.Request, row DedupeCandidateRow) {
	c, err := toContractDedupeCandidate(row)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, c)
}

// toContractDedupeCandidate renders one row; the stored evidence jsonb IS
// the wire evidence — decoded into the contract's typed items, never
// recomputed.
func toContractDedupeCandidate(row DedupeCandidateRow) (crmcontracts.DedupeCandidate, error) {
	c := crmcontracts.DedupeCandidate{
		Id:         openapi_types.UUID(row.ID),
		EntityType: crmcontracts.DedupeCandidateEntityType(row.EntityType),
		LeftId:     openapi_types.UUID(row.LeftID),
		RightId:    openapi_types.UUID(row.RightID),
		Confidence: float32(row.Confidence),
		Status:     crmcontracts.DedupeCandidateStatus(row.Disposition),
		CreatedAt:  row.CreatedAt,
		DisposedAt: row.DisposedAt,
	}
	if row.DisposedBy != nil {
		u := openapi_types.UUID(*row.DisposedBy)
		c.DisposedBy = &u
	}
	if len(row.Evidence) > 0 {
		if err := json.Unmarshal(row.Evidence, &c.Evidence); err != nil {
			return crmcontracts.DedupeCandidate{}, err
		}
	}
	return c, nil
}
