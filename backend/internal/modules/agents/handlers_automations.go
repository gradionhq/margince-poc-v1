// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// The automations transport slice (B-E15.4): compose embeds Handlers so
// the generated stubs are shadowed. Mutations are contract-annotated
// human-only; the store re-gates on the `automation` RBAC object.

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// pathID asserts a contract path id as entity K's id — the widening
// point between the wire and the typed store surface (the route already
// names the entity, so the assertion lives here, not in the store).
func pathID[K ids.EntityKind](id crmcontracts.Id) ids.ID[K] {
	return ids.From[K](ids.UUID(id))
}

// Handlers is the agents module's HTTP surface.
type Handlers struct {
	automations *AutomationStore
}

func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{automations: NewAutomationStore(pool)}
}

// ListAutomationCatalog implements (GET /automations/catalog).
func (h Handlers) ListAutomationCatalog(w http.ResponseWriter, r *http.Request) {
	entries := Catalog()
	data := make([]crmcontracts.AutomationCatalogEntry, 0, len(entries))
	for _, e := range entries {
		entry := crmcontracts.AutomationCatalogEntry{
			Key:          e.Key,
			Name:         e.Name,
			Trigger:      e.Trigger,
			Action:       e.Action,
			ParamsSchema: e.ParamsSchema,
		}
		entry.Description = &e.Description
		tier := crmcontracts.AutomationCatalogEntryTier(e.Tier)
		entry.Tier = &tier
		data = append(data, entry)
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.AutomationCatalogEntry `json:"data"`
	}{Data: data})
}

// ListAutomations implements (GET /automations).
func (h Handlers) ListAutomations(w http.ResponseWriter, r *http.Request, params crmcontracts.ListAutomationsParams) {
	page, err := h.automations.List(r.Context(), params.Cursor, params.Limit)
	if err != nil {
		writeAutomationErr(w, r, err)
		return
	}
	data := make([]crmcontracts.Automation, 0, len(page.Items))
	for _, a := range page.Items {
		wire, err := wireAutomation(a)
		if err != nil {
			httperr.Write(w, r, err)
			return
		}
		data = append(data, wire)
	}
	resp := struct {
		Data []crmcontracts.Automation `json:"data"`
		Page crmcontracts.PageInfo     `json:"page"`
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: page.HasMore}}
	if page.NextCursor != "" {
		resp.Page.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// CreateAutomation implements (POST /automations): created paused.
func (h Handlers) CreateAutomation(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.CreateAutomationRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	created, err := h.automations.Create(r.Context(), CreateAutomationInput{
		Key: req.Key, Name: req.Name, Params: req.Params,
	})
	if err != nil {
		writeAutomationErr(w, r, err)
		return
	}
	wire, err := wireAutomation(created)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusCreated, wire)
}

// GetAutomation implements (GET /automations/{id}).
func (h Handlers) GetAutomation(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	a, err := h.automations.Get(r.Context(), pathID[ids.AutomationKind](id))
	if err != nil {
		writeAutomationErr(w, r, err)
		return
	}
	wire, err := wireAutomation(a)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wire)
}

// UpdateAutomation implements (PATCH /automations/{id}): params, name,
// and the enabled/paused flip, under If-Match.
func (h Handlers) UpdateAutomation(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, _ crmcontracts.UpdateAutomationParams) {
	ifVersion, ok := httperr.IfMatchVersion(w, r)
	if !ok {
		return
	}
	var req crmcontracts.UpdateAutomationRequest
	if !httperr.Decode(w, r, &req) {
		return
	}
	in := UpdateAutomationInput{Name: req.Name, IfVersion: ifVersion}
	if req.Params != nil {
		in.Params = *req.Params
	}
	if req.Status != nil {
		switch *req.Status {
		case "enabled":
			enabled := true
			in.Enabled = &enabled
		case "paused":
			enabled := false
			in.Enabled = &enabled
		default:
			httperr.Write(w, r, httperr.Validation("status", "invalid", "status is enabled or paused"))
			return
		}
	}
	updated, err := h.automations.Update(r.Context(), pathID[ids.AutomationKind](id), in)
	if err != nil {
		writeAutomationErr(w, r, err)
		return
	}
	wire, err := wireAutomation(updated)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, wire)
}

// DeleteAutomation implements (DELETE /automations/{id}): soft archive.
func (h Handlers) DeleteAutomation(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	if err := h.automations.Archive(r.Context(), pathID[ids.AutomationKind](id)); err != nil {
		writeAutomationErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeAutomationErr(w http.ResponseWriter, r *http.Request, err error) {
	var param *ParamError
	if errors.As(err, &param) {
		httperr.Write(w, r, httperr.Validation(param.Field, "invalid", param.Reason))
		return
	}
	httperr.Write(w, r, err)
}

func wireAutomation(a Automation) (crmcontracts.Automation, error) {
	status := crmcontracts.AutomationStatus("paused")
	if a.Enabled {
		status = crmcontracts.AutomationStatus("enabled")
	}
	params := map[string]interface{}{}
	if len(a.Params) > 0 {
		if err := json.Unmarshal(a.Params, &params); err != nil {
			return crmcontracts.Automation{}, err
		}
	}
	version := int(a.Version)
	return crmcontracts.Automation{
		Id:        openapi_types.UUID(a.ID.UUID),
		Key:       a.Key,
		Name:      a.Name,
		Status:    status,
		Params:    params,
		Version:   &version,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}, nil
}
