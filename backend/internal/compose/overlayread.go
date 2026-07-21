// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The overlay-mode human read surface (design.md §4.1: "Overlay does not
// fork the data API"). Server shadows the contract read ops for the five
// mirror entity types — get/list for person, organization, deal, lead,
// activity, plus search — routing them through the same Dispatcher the
// MCP/agent seam consumers already ride when the workspace runs in
// overlay mode, and delegating to the native module handler otherwise.
// Visibility (the fail-closed deny-join) and freshness are applied
// inside overlay.Provider; what lives here is mode dispatch, the honest
// refusal of list dials the mirror cannot answer, and the typed wire
// assembly (overlaywire.go).
//
// List/search filters the mirror does not hold (owner_id, tag, status,
// sort, …) answer 422 naming the parameter — never a silently-ignored
// dial that quietly returns the unfiltered world. q rides the overlay
// provider's substring filter; include_archived is accepted because the
// mirror holds no archived rows at all (a tombstoned incumbent record is
// deleted, not archived), so both values honestly answer the same page.

import (
	"context"
	"errors"
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// The refused list/search parameter names that recur across the shadows
// below — named once so each refusal spells the contract's own query
// vocabulary identically.
const (
	paramSort           = "sort"
	paramOwnerID        = "owner_id"
	paramPipelineID     = "pipeline_id"
	paramStageID        = "stage_id"
	paramOrganizationID = "organization_id"
	paramStatus         = "status"
	paramKind           = "kind"
	paramTag            = "tag"
)

// overlayParam pairs one refused query-parameter name with whether the
// request set it.
type overlayParam struct {
	name string
	set  bool
}

// overlayReadMode answers whether this request dispatches to the mirror.
// A mode-resolution failure is written to w (ok=false): serving native
// data to an overlay workspace because the mode lookup failed would be
// the silent-fallback the overlay module exists to refuse.
func (s Server) overlayReadMode(w http.ResponseWriter, r *http.Request) (overlayMode, ok bool) {
	ov, err := s.sorDispatch.isOverlay(r.Context())
	if err != nil {
		httperr.Write(w, r, err)
		return false, false
	}
	return ov, true
}

// unsupportedOverlayParam refuses one list/search dial the mirror cannot
// answer — 422 naming the parameter, the same shape every other bad
// query input uses.
func unsupportedOverlayParam(w http.ResponseWriter, r *http.Request, name string) {
	httperr.Write(w, r, httperr.Validation(name, "unsupported_in_overlay_mode",
		"this parameter is not available while the workspace reads from the incumbent mirror — drop it, or read through the incumbent's own UI"))
}

// overlayGet serves one GET-by-id shadow: the native handler off overlay
// mode, otherwise a dispatched mirror Read assembled by wire. A miss (or
// an unmapped caller's existence-hiding deny) stays the 404 the sentinel
// mapping renders.
func overlayGet[T any](s Server, w http.ResponseWriter, r *http.Request, et datasource.EntityType, id crmcontracts.Id,
	native func(), wire func(context.Context, datasource.Record) (T, error),
) {
	ov, ok := s.overlayReadMode(w, r)
	if !ok {
		return
	}
	if !ov {
		native()
		return
	}
	// The same object-capability gate the native handler applies (403 on
	// denial) — the mirror's visibility deny-join is row-scope, not a
	// substitute for object RBAC, and both modes must answer one contract
	// the same way. Entity-type strings ARE the RBAC object names.
	if err := auth.Require(r.Context(), string(et), principal.ActionRead); err != nil {
		httperr.Write(w, r, err)
		return
	}
	rec, err := s.sorDispatch.Read(r.Context(), datasource.EntityRef{Type: et, ID: ids.UUID(id)})
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	body, err := wire(r.Context(), rec)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	httperr.WriteJSON(w, http.StatusOK, body)
}

// overlayList serves one list shadow: the native handler off overlay
// mode; otherwise refuse any set parameter the mirror cannot answer,
// page the visibility-joined mirror, and assemble each record through
// wire into respond's typed list body. An unmapped caller's
// existence-hiding ErrNotFound answers an EMPTY page here: on the native
// path a collection read row-scopes down to nothing rather than 404ing,
// and the two modes must answer one contract the same way (GET-by-id
// keeps the 404).
func overlayList[T any](s Server, w http.ResponseWriter, r *http.Request, et datasource.EntityType,
	native func(), refuse []overlayParam, q, cursor *string, limit *int,
	wire func(context.Context, datasource.Record) (T, error),
	respond func([]T, crmcontracts.PageInfo) any,
) {
	ov, ok := s.overlayReadMode(w, r)
	if !ok {
		return
	}
	if !ov {
		native()
		return
	}
	// Object RBAC before any parameter shaping — same gate and order the
	// native list handlers apply (overlayGet's own rationale).
	if err := auth.Require(r.Context(), string(et), principal.ActionRead); err != nil {
		httperr.Write(w, r, err)
		return
	}
	for _, p := range refuse {
		if p.set {
			unsupportedOverlayParam(w, r, p.name)
			return
		}
	}
	query := datasource.SearchQuery{EntityTypes: []datasource.EntityType{et}}
	if q != nil {
		query.Text = *q
	}
	if cursor != nil {
		query.Cursor = *cursor
	}
	if limit != nil {
		query.Limit = *limit
	}
	res, err := s.sorDispatch.Search(r.Context(), query)
	if err != nil && !errors.Is(err, apperrors.ErrNotFound) {
		httperr.Write(w, r, err)
		return
	}
	data := make([]T, 0, len(res.Records))
	for _, rec := range res.Records {
		body, wireErr := wire(r.Context(), rec)
		if wireErr != nil {
			httperr.Write(w, r, wireErr)
			return
		}
		data = append(data, body)
	}
	page := crmcontracts.PageInfo{HasMore: res.HasMore}
	if res.NextCursor != "" {
		page.NextCursor = &res.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, respond(data, page))
}

// GetPerson shadows the person read: mirror-assembled in overlay mode,
// the native people handler otherwise. Same split for every Get/List
// shadow below.
func (s Server) GetPerson(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlayGet(s, w, r, datasource.EntityPerson, id,
		func() { s.peopleHandlers.GetPerson(w, r, id) }, overlayWirePerson)
}

// ListPeople shadows the person list.
func (s Server) ListPeople(w http.ResponseWriter, r *http.Request, params crmcontracts.ListPeopleParams) {
	overlayList(s, w, r, datasource.EntityPerson,
		func() { s.peopleHandlers.ListPeople(w, r, params) },
		[]overlayParam{{paramSort, params.Sort != nil}, {paramOwnerID, params.OwnerId != nil}, {paramTag, params.Tag != nil}},
		params.Q, params.Cursor, params.Limit, overlayWirePerson,
		func(data []crmcontracts.Person, page crmcontracts.PageInfo) any {
			return crmcontracts.PersonListResponse{Data: data, Page: page}
		})
}

// GetOrganization shadows the organization read.
func (s Server) GetOrganization(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlayGet(s, w, r, datasource.EntityOrganization, id,
		func() { s.peopleHandlers.GetOrganization(w, r, id) }, overlayWireOrganization)
}

// ListOrganizations shadows the organization list.
func (s Server) ListOrganizations(w http.ResponseWriter, r *http.Request, params crmcontracts.ListOrganizationsParams) {
	overlayList(s, w, r, datasource.EntityOrganization,
		func() { s.peopleHandlers.ListOrganizations(w, r, params) },
		[]overlayParam{{paramSort, params.Sort != nil}, {paramOwnerID, params.OwnerId != nil}, {"domain", params.Domain != nil}},
		params.Q, params.Cursor, params.Limit, overlayWireOrganization,
		func(data []crmcontracts.Organization, page crmcontracts.PageInfo) any {
			return crmcontracts.OrganizationListResponse{Data: data, Page: page}
		})
}

// GetDeal shadows the deal read.
func (s Server) GetDeal(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlayGet(s, w, r, datasource.EntityDeal, id,
		func() { s.dealsHandlers.GetDeal(w, r, id) }, overlayWireDeal)
}

// ListDeals shadows the deal list. The deal list has no q parameter, so
// none rides the mirror page either.
func (s Server) ListDeals(w http.ResponseWriter, r *http.Request, params crmcontracts.ListDealsParams) {
	overlayList(s, w, r, datasource.EntityDeal,
		func() { s.dealsHandlers.ListDeals(w, r, params) },
		[]overlayParam{
			{paramSort, params.Sort != nil},
			{paramPipelineID, params.PipelineId != nil},
			{paramStageID, params.StageId != nil},
			{paramOwnerID, params.OwnerId != nil},
			{paramOrganizationID, params.OrganizationId != nil},
			{paramStatus, params.Status != nil},
			{"stalled", params.Stalled != nil},
			{"partner_org_id", params.PartnerOrgId != nil},
			{"partner_sourced", params.PartnerSourced != nil},
		},
		nil, params.Cursor, params.Limit, overlayWireDeal,
		func(data []crmcontracts.Deal, page crmcontracts.PageInfo) any {
			return crmcontracts.DealListResponse{Data: data, Page: page}
		})
}

// GetLead shadows the lead read.
func (s Server) GetLead(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlayGet(s, w, r, datasource.EntityLead, id,
		func() { s.peopleHandlers.GetLead(w, r, id) }, overlayWireLead)
}

// ListLeads shadows the lead list.
func (s Server) ListLeads(w http.ResponseWriter, r *http.Request, params crmcontracts.ListLeadsParams) {
	overlayList(s, w, r, datasource.EntityLead,
		func() { s.peopleHandlers.ListLeads(w, r, params) },
		[]overlayParam{{paramSort, params.Sort != nil}, {paramStatus, params.Status != nil}, {paramOwnerID, params.OwnerId != nil}, {"min_score", params.MinScore != nil}},
		params.Q, params.Cursor, params.Limit, overlayWireLead,
		func(data []crmcontracts.Lead, page crmcontracts.PageInfo) any {
			return crmcontracts.LeadListResponse{Data: data, Page: page}
		})
}

// GetActivity shadows the activity read.
func (s Server) GetActivity(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	overlayGet(s, w, r, datasource.EntityActivity, id,
		func() { s.activitiesHandlers.GetActivity(w, r, id) }, overlayWireActivity)
}

// ListActivities shadows the activity list.
func (s Server) ListActivities(w http.ResponseWriter, r *http.Request, params crmcontracts.ListActivitiesParams) {
	overlayList(s, w, r, datasource.EntityActivity,
		func() { s.activitiesHandlers.ListActivities(w, r, params) },
		[]overlayParam{
			{paramSort, params.Sort != nil},
			{paramKind, params.Kind != nil},
			{"entity_type", params.EntityType != nil},
			{"entity_id", params.EntityId != nil},
			{"assignee_id", params.AssigneeId != nil},
		},
		params.Q, params.Cursor, params.Limit, overlayWireActivity,
		func(data []crmcontracts.Activity, page crmcontracts.PageInfo) any {
			return crmcontracts.ActivityListResponse{Data: data, Page: page}
		})
}

// overlaySearchTypes is the entity-type order the overlay search union
// walks — fixed, so a capped page is deterministic.
var overlaySearchTypes = []datasource.EntityType{
	datasource.EntityPerson,
	datasource.EntityOrganization,
	datasource.EntityDeal,
	datasource.EntityLead,
	datasource.EntityActivity,
}

// overlaySearchDefaultLimit caps an overlay search page when the request
// names no limit — the contract's documented default page size.
const overlaySearchDefaultLimit = 25

// overlaySearchMaxLimit is the contract's SearchParams ceiling (crm.yaml:
// limit maximum 100). A bound integer that slips past request validation
// (a negative or oversized ?limit=) must never reach a slice capacity, so
// the value is clamped here before it sizes any allocation.
const overlaySearchMaxLimit = 100

// clampOverlaySearchLimit maps a caller-supplied limit onto the contract's
// 1..100 range so it is safe to use as an allocation size.
func clampOverlaySearchLimit(v int) int {
	switch {
	case v < 1:
		return 1
	case v > overlaySearchMaxLimit:
		return overlaySearchMaxLimit
	default:
		return v
	}
}

// Search shadows the global search: in overlay mode it is a best-effort
// visibility-filtered union across entity types (design.md §4.5) — a
// single capped page with no cross-type cursor, so a supplied cursor is
// refused rather than silently restarting the walk.
func (s Server) Search(w http.ResponseWriter, r *http.Request, params crmcontracts.SearchParams) {
	ov, ok := s.overlayReadMode(w, r)
	if !ok {
		return
	}
	if !ov {
		s.searchHandlers.Search(w, r, params)
		return
	}
	if params.Cursor != nil {
		unsupportedOverlayParam(w, r, "cursor")
		return
	}
	types := overlaySearchTypes
	if params.Types != nil {
		types = make([]datasource.EntityType, 0, len(*params.Types))
		for _, t := range *params.Types {
			types = append(types, datasource.EntityType(t))
		}
	}
	limit := overlaySearchDefaultLimit
	if params.Limit != nil {
		limit = clampOverlaySearchLimit(*params.Limit)
	}
	hits := make([]crmcontracts.SearchResult, 0, limit)
	// hasMore turns true when the page filled before every requested type
	// was fully walked — there is no cross-type cursor to resume with, so
	// the flag is the one honest signal that narrowing the query would
	// surface more.
	hasMore := false
	for _, et := range types {
		if len(hits) >= limit {
			hasMore = true
			break
		}
		typed, more, err := s.overlaySearchOneType(r.Context(), et, params.Q, limit-len(hits))
		if err != nil {
			httperr.Write(w, r, err)
			return
		}
		hits = append(hits, typed...)
		if more {
			hasMore = true
		}
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.SearchResponse{Data: hits, Page: crmcontracts.PageInfo{HasMore: hasMore}})
}

// overlaySearchOneType pages one entity type's visibility-joined mirror
// hits for the overlay search union, titled per type. A type the caller
// may not read answers empty, not a query-wide 403 — search shows only
// the object classes the seat can read, the native surface's own
// posture; an unmapped caller likewise sees the empty world
// (overlayList's rationale). Anything else is a real failure and
// surfaces.
func (s Server) overlaySearchOneType(ctx context.Context, et datasource.EntityType, text string, remaining int) (typed []crmcontracts.SearchResult, hasMore bool, err error) {
	if err := auth.Require(ctx, string(et), principal.ActionRead); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return nil, false, nil
		}
		return nil, false, err
	}
	res, err := s.sorDispatch.Search(ctx, datasource.SearchQuery{
		Text:        text,
		EntityTypes: []datasource.EntityType{et},
		Limit:       remaining,
	})
	if err != nil {
		if errors.Is(err, apperrors.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	typed = ContractSearchResults(res)
	for i, rec := range res.Records {
		fields, fieldsErr := overlayRecordFields(rec)
		if fieldsErr != nil {
			return nil, false, fieldsErr
		}
		if title := overlayWireTitle(et, fields); title != "" {
			typed[i].Title = &title
		}
	}
	return typed, res.HasMore, nil
}
