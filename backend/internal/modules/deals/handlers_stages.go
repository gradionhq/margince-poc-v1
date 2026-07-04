// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) UpdatePipeline(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdatePipelineParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdatePipelineRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	pipeline, err := h.store.UpdatePipeline(r.Context(), ids.UUID(id), UpdatePipelineInput{
		Name: req.Name, IsDefault: req.IsDefault, Position: req.Position, IfVersion: ifVersion,
	})
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, pipeline)
}

func (h Handlers) ListStages(w http.ResponseWriter, r *http.Request, params crmcontracts.ListStagesParams) {
	var pipelineID *ids.UUID
	if params.PipelineId != nil {
		id := ids.UUID(*params.PipelineId)
		pipelineID = &id
	}
	includeArchived := params.IncludeArchived != nil && *params.IncludeArchived
	stages, err := h.store.ListStages(r.Context(), pipelineID, includeArchived)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	if stages == nil {
		stages = []crmcontracts.Stage{}
	}
	httperr.WriteJSON(w, http.StatusOK, map[string]any{"data": stages, "page": crmcontracts.PageInfo{}})
}

func (h Handlers) CreateStage(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreateStageParams) {
	var req crmcontracts.CreateStageRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := CreateStageInput{
		PipelineID:     ids.UUID(req.PipelineId),
		Name:           req.Name,
		Position:       req.Position,
		WinProbability: req.WinProbability,
	}
	if req.Semantic != nil {
		in.Semantic = string(*req.Semantic)
	}
	stage, err := h.store.CreateStage(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, stage)
}

func (h Handlers) GetStage(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	stage, err := h.store.GetStage(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, stage)
}

func (h Handlers) UpdateStage(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateStageParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateStageRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateStageInput{
		Name:           req.Name,
		Position:       req.Position,
		WinProbability: req.WinProbability,
		IfVersion:      ifVersion,
	}
	if req.Semantic != nil {
		semantic := string(*req.Semantic)
		in.Semantic = &semantic
	}
	stage, err := h.store.UpdateStage(r.Context(), ids.UUID(id), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, stage)
}
