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
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
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

// PreviewAutomation implements (POST /automations/{id}/preview): the
// designer's dry-run blast radius (A72/ADR-0035 Am.1). A 🟢 read — the
// store evaluates the When/If without applying, staging, or sending. The
// body is optional: absent previews the stored instance as-is, present
// previews a draft recipe before it is saved.
func (h Handlers) PreviewAutomation(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	var req crmcontracts.AutomationPreviewRequest
	if r.ContentLength != 0 {
		if !httperr.Decode(w, r, &req) {
			return
		}
	}
	in := AutomationPreviewInput{Key: req.Key, WindowDays: req.WindowDays}
	if req.Params != nil {
		in.Params = *req.Params
	}
	res, err := h.automations.Preview(r.Context(), pathID[ids.AutomationKind](id), in)
	if err != nil {
		writeAutomationErr(w, r, err)
		return
	}
	wire := crmcontracts.AutomationPreview{
		MatchesNow:     res.MatchesNow,
		WindowDays:     res.WindowDays,
		WouldHaveFired: res.WouldHaveFired,
	}
	// The masked count ships even at zero: "nothing was hidden from you"
	// is information, not noise.
	excluded := res.ExcludedByPermission
	wire.ExcludedByPermission = &excluded
	sample := make([]openapi_types.UUID, 0, len(res.Sample))
	for _, sid := range res.Sample {
		sample = append(sample, openapi_types.UUID(sid))
	}
	wire.Sample = &sample
	httperr.WriteJSON(w, http.StatusOK, wire)
}

// ListAutomationRuns implements (GET /automations/{id}/runs): the honest
// run history — every outcome including failed/blocked/skipped, each
// with its reason (A72/ADR-0035 Am.1, B-E15.3a).
func (h Handlers) ListAutomationRuns(w http.ResponseWriter, r *http.Request, id crmcontracts.Id, params crmcontracts.ListAutomationRunsParams) {
	var outcome *string
	if params.Outcome != nil {
		s := string(*params.Outcome)
		outcome = &s
	}
	page, err := h.automations.ListRuns(r.Context(), pathID[ids.AutomationKind](id), params.Cursor, params.Limit, outcome)
	if err != nil {
		writeAutomationErr(w, r, err)
		return
	}
	data := make([]crmcontracts.AutomationRun, 0, len(page.Items))
	for _, rec := range page.Items {
		data = append(data, wireAutomationRun(id, rec))
	}
	resp := struct {
		Data []crmcontracts.AutomationRun `json:"data"`
		Page crmcontracts.PageInfo        `json:"page"`
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: page.HasMore}}
	if page.NextCursor != "" {
		resp.Page.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// wireAutomationRun renders one run record in the contract shape: the
// reason rides only the reasoned outcomes, and the target/action lines
// come from the run's own planned/applied trace.
func wireAutomationRun(automationID crmcontracts.Id, rec AutomationRunRecord) crmcontracts.AutomationRun {
	run := crmcontracts.AutomationRun{
		Id:           openapi_types.UUID(rec.ID),
		AutomationId: openapi_types.UUID(automationID),
		OccurredAt:   rec.CreatedAt,
		Outcome:      crmcontracts.AutomationRunOutcome(rec.Outcome()),
		Tier:         crmcontracts.AutomationRunTier(rec.Tier),
	}
	switch rec.Status {
	case "failed", "blocked", "skipped":
		run.Reason = rec.Detail
	}
	if rec.Status == "requires_approval" || rec.Status == "blocked" {
		approvalRequired := true
		run.ApprovalRequired = &approvalRequired
	}
	evidence := "triggered by event " + rec.TriggerEvent.String()
	run.TriggerEvidence = &evidence
	if result := runActionResult(rec); result != "" {
		run.ActionResult = &result
	}
	if target := runTargetRef(rec); target != "" {
		run.TargetRef = &target
	}
	return run
}

// runActionResult summarizes what the run did in the contract's
// one-liner: the applied action kinds for a fired run, the inbox queue
// for a staged one; the reasoned outcomes speak through Reason instead.
func runActionResult(rec AutomationRunRecord) string {
	switch rec.Status {
	case "applied":
		kinds := runActionKinds(rec.Applied)
		if len(kinds) == 0 {
			return "applied"
		}
		return "applied " + strings.Join(kinds, ", ")
	case "requires_approval":
		return "queued to approval inbox"
	default:
		return ""
	}
}

// runTargetRef names the record the run acted on — the first planned
// action's target, as "type:id".
func runTargetRef(rec AutomationRunRecord) string {
	var actions []workflow.Action
	if err := json.Unmarshal(rec.Planned, &actions); err != nil || len(actions) == 0 {
		// A pre-Plan run (skipped, failed at Match) has an empty plan —
		// there is honestly no target to name.
		return ""
	}
	target := actions[0].Target
	if target.ID == ids.Nil {
		return ""
	}
	return string(target.Type) + ":" + target.ID.String()
}

// runActionKinds lists the distinct action kinds of a run trace, in
// trace order.
func runActionKinds(trace json.RawMessage) []string {
	var actions []workflow.Action
	if err := json.Unmarshal(trace, &actions); err != nil {
		return nil
	}
	var kinds []string
	seen := map[workflow.ActionKind]bool{}
	for _, a := range actions {
		if !seen[a.Kind] {
			seen[a.Kind] = true
			kinds = append(kinds, string(a.Kind))
		}
	}
	return kinds
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
