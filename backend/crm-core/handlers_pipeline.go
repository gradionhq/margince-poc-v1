package crmcore

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/crm-contracts"
	"github.com/gradionhq/margince/backend/crm-core/internal/store"
	"github.com/gradionhq/margince/backend/internal/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (h Handlers) ListPipelines(w http.ResponseWriter, r *http.Request, _ crmcontracts.ListPipelinesParams) {
	pipelines, err := h.store.ListPipelines(r.Context())
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, crmcontracts.PipelineListResponse{
		Data: pipelines,
		Page: crmcontracts.PageInfo{HasMore: false},
	})
}

func (h Handlers) CreatePipeline(w http.ResponseWriter, r *http.Request, _ crmcontracts.CreatePipelineParams) {
	var req crmcontracts.CreatePipelineRequest
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		httperr.Write(w, r, httperr.Validation("name", "required", "name is required"))
		return
	}

	in := store.CreatePipelineInput{
		Name:      req.Name,
		IsDefault: req.IsDefault != nil && *req.IsDefault,
	}
	if req.Position != nil {
		in.Position = *req.Position
	}
	if req.Stages != nil {
		for i, st := range *req.Stages {
			stage := store.StageInput{Name: st.Name, Position: st.Position, Semantic: "open"}
			if stage.Position == 0 {
				stage.Position = i + 1
			}
			if st.Semantic != nil {
				stage.Semantic = string(*st.Semantic)
			}
			if st.WinProbability != nil {
				stage.WinProbability = *st.WinProbability
			}
			in.Stages = append(in.Stages, stage)
		}
	}

	pipeline, err := h.store.CreatePipeline(r.Context(), in)
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/pipelines/"+pipeline.Id.String())
	writeJSON(w, http.StatusCreated, pipeline)
}

func (h Handlers) GetPipeline(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	pipeline, err := h.store.GetPipeline(r.Context(), ids.UUID(id))
	if err != nil {
		writeStoreErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pipeline)
}
