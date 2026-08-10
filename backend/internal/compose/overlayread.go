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
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
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
	// paramCapturedByKind is refused in overlay mode rather than ignored:
	// captured_by is OUR provenance column, stamped from the principal that
	// wrote the row. Mirror rows are the incumbent's records, created in the
	// incumbent by whoever uses it, so "which of these did our AI create?" has
	// no answer there. Answering the whole mirror would present an unfiltered
	// list as the review list.
	paramCapturedByKind = "captured_by_kind"
	// paramAiWritten is refused for the same reason as paramCapturedByKind, and
	// more plainly: it is derived from OUR per-value provenance rows, which a
	// mirror of the incumbent's records simply does not have.
	paramAiWritten = "ai_written"
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
		[]overlayParam{
			{paramSort, params.Sort != nil},
			{paramOwnerID, params.OwnerId != nil},
			{paramTag, params.Tag != nil},
			{paramCapturedByKind, params.CapturedByKind != nil},
			{paramAiWritten, params.AiWritten != nil},
		},
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
		[]overlayParam{
			{paramSort, params.Sort != nil},
			{paramOwnerID, params.OwnerId != nil},
			{"domain", params.Domain != nil},
			{paramCapturedByKind, params.CapturedByKind != nil},
			{paramAiWritten, params.AiWritten != nil},
			// Where the account stands and what it is to us are OUR columns.
			// The mirror has neither, so filtering on them would silently
			// return the unfiltered list — an answer that reads as a filtered
			// one and is not.
			{"lifecycle", params.Lifecycle != nil},
			{"relationship_type", params.RelationshipType != nil},
			// The installation's own company is a NATIVE row; the mirror holds
			// the incumbent's accounts and never carries it. Answering the
			// opt-in with a page that could not contain the anchor either way
			// would read as satisfied and is not (ADR-0082/A127).
			{"include_anchor", params.IncludeAnchor != nil},
		},
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
		[]overlayParam{
			{paramSort, params.Sort != nil},
			{paramStatus, params.Status != nil},
			{paramOwnerID, params.OwnerId != nil},
			{"min_score", params.MinScore != nil},
			{paramCapturedByKind, params.CapturedByKind != nil},
			{paramAiWritten, params.AiWritten != nil},
		},
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
			// thread_key is how the company timeline completes a group the
			// page cut off. The mirror does not carry the provider thread, so
			// answering it unfiltered would hand back unrelated items as if
			// they were the rest of that conversation.
			{"thread_key", params.ThreadKey != nil},
		},
		params.Q, params.Cursor, params.Limit, overlayWireActivity,
		func(data []crmcontracts.Activity, page crmcontracts.PageInfo) any {
			return crmcontracts.ActivityListResponse{Data: data, Page: page}
		})
}

// overlaySearchTypes is the entity-type order the overlay search walks. It is
// the MODULE's own list rather than a copy: the provider refuses a class the
// mirror cannot hold, and a second list here would let this door refuse one it
// can, or admit one it cannot, the moment a sixth is mirrored.
var overlaySearchTypes = overlay.MirroredEntityTypes()

// overlayMirroredTypes is the set of record types the mirror holds, keyed by
// the string form that is both datasource.EntityType and the generated
// agentPolicy.RecordType. Derived from overlaySearchTypes rather than
// re-listed, so reads and writes cannot drift.
var overlayMirroredTypes = func() map[string]bool {
	set := make(map[string]bool, len(overlaySearchTypes))
	for _, et := range overlaySearchTypes {
		set[string(et)] = true
	}
	return set
}()

// overlaySearchDefaultLimit sizes an overlay search page when the request
// names no limit — the shared Limit parameter's own default (crm.yaml's
// components.parameters.Limit: default 50), which /search refs like every
// other paged op. Overlay pages the same way native does or the two modes
// answer different pages for one query.
const overlaySearchDefaultLimit = 50

// overlaySearchMaxLimit is that same shared parameter's ceiling (maximum
// 200). A bound integer that slips past request validation (a negative or
// oversized ?limit=) must never reach a slice capacity, so the value is
// clamped here before it sizes any allocation.
const overlaySearchMaxLimit = 200

// clampOverlaySearchLimit maps a caller-supplied limit onto the shared
// parameter's 1..200 range so it is safe to use as an allocation size.
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

// Search shadows the global search: in overlay mode it is a
// visibility-filtered walk across entity types (design.md §4.5), served by
// the provider's own sweep so the MCP tool and this route answer one
// implementation rather than two.
//
// It PAGES. The walk has no ranking to interleave types by, so the sweep's
// cursor names where it stopped — the type plus that type's own mirror
// cursor — and `has_more` is true exactly when there is such a position to
// hand back.
func (s Server) Search(w http.ResponseWriter, r *http.Request, params crmcontracts.SearchParams) {
	ov, ok := s.overlayReadMode(w, r)
	if !ok {
		return
	}
	if !ov {
		s.searchHandlers.Search(w, r, params)
		return
	}
	types, ok := s.overlaySearchScope(w, r, params.Types)
	if !ok {
		return
	}
	query := datasource.SearchQuery{Text: params.Q, EntityTypes: types}
	if params.Cursor != nil {
		query.Cursor = *params.Cursor
	}
	if params.Limit != nil {
		query.Limit = clampOverlaySearchLimit(*params.Limit)
	} else {
		query.Limit = overlaySearchDefaultLimit
	}
	// An empty scope is an answerable question with an empty answer: the one
	// type the caller named is one they may not read. Serving it here rather
	// than through the provider is what keeps it a page instead of the 403 the
	// seam answers a tool with (overlaySearchScope's own rationale).
	res := datasource.SearchResult{}
	if len(types) > 0 {
		// An unmapped caller's existence-hiding ErrNotFound answers an EMPTY
		// page here, the same reading every list shadow gives it: a collection
		// read row-scopes down to nothing rather than 404ing, and both modes
		// must answer one contract the same way.
		var err error
		if res, err = s.sorDispatch.Search(r.Context(), query); err != nil && !errors.Is(err, apperrors.ErrNotFound) {
			httperr.Write(w, r, err)
			return
		}
	}
	hits, err := overlaySearchHits(res)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	page := crmcontracts.PageInfo{HasMore: res.HasMore}
	if res.NextCursor != "" {
		page.NextCursor = &res.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.SearchResponse{Data: hits, Page: page})
}

// overlaySearchScope resolves which entity types this request sweeps.
//
// A named type the mirror does not hold is REFUSED rather than walked past.
// The mirror carries five object classes and the contract's `types` enum
// carries six, so a caller asking for projects would otherwise be handed an
// empty page reading "this workspace has no projects" — an answer about the
// records, when the truth is about the mode.
//
// It resolves ONE denial itself and leaves the rest to the provider, because
// the two doors answer a denial differently and each rule belongs where it is
// kept. Search shows a seat the object classes it can read and says nothing
// about the rest, so a caller who named a single type they may not read gets
// an empty page here rather than the 403 the seam gives a tool. Everything
// wider goes through unfiltered: the provider omits what the seat cannot see,
// and narrowing the list here as well would make the sweep's own posture
// depend on a filter applied a layer above it.
func (s Server) overlaySearchScope(
	w http.ResponseWriter, r *http.Request, named *[]crmcontracts.SearchParamsTypes,
) ([]datasource.EntityType, bool) {
	asked := overlaySearchTypes
	if named != nil {
		asked = make([]datasource.EntityType, 0, len(*named))
		for _, t := range *named {
			et := datasource.EntityType(t)
			if !overlayMirroredTypes[string(et)] {
				unsupportedOverlayParam(w, r, "types")
				return nil, false
			}
			asked = append(asked, et)
		}
	}
	if len(asked) != 1 {
		return asked, true
	}
	err := auth.Require(r.Context(), string(asked[0]), principal.ActionRead)
	switch {
	case err == nil:
		return asked, true
	case errors.Is(err, apperrors.ErrPermissionDenied):
		// The empty scope, which Search answers as an empty page.
		return nil, true
	default:
		// Not a fact about the caller's grants — this server not working.
		// Reading it as "may not see it" would answer a broken request chain
		// with an empty page and a 200.
		httperr.Write(w, r, err)
		return nil, false
	}
}

// overlaySearchHits assembles one swept page onto the wire, titling each hit
// by the type it came from.
func overlaySearchHits(res datasource.SearchResult) ([]crmcontracts.SearchResult, error) {
	typed := ContractSearchResults(res)
	for i, rec := range res.Records {
		fields, err := overlayRecordFields(rec)
		if err != nil {
			return nil, err
		}
		if title := overlayWireTitle(rec.Ref.Type, fields); title != "" {
			typed[i].Title = &title
		}
	}
	return typed, nil
}
