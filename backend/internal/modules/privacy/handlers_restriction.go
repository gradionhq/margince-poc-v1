// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// The restricted-records transport (A165/ADR-0114 §4): the controller's review
// surface over what a statutory obligation is holding.

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// ListRestrictedActivities implements (GET /retention/restrictions).
func (h Handlers) ListRestrictedActivities(w http.ResponseWriter, r *http.Request, params crmcontracts.ListRestrictedActivitiesParams) {
	page, err := ListRestrictedActivities(r.Context(), h.db, params.Cursor, params.Limit)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.RestrictedRecord, 0, len(page.Records))
	for _, record := range page.Records {
		data = append(data, restrictedRecordToWire(record))
	}
	resp := struct {
		Data []crmcontracts.RestrictedRecord `json:"data"`
		Page crmcontracts.PageInfo           `json:"page"`
	}{Data: data, Page: crmcontracts.PageInfo{HasMore: page.HasMore}}
	if page.NextCursor != "" {
		resp.Page.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, resp)
}

// qualifyingRecordWire is the generated RestrictedRecord.Deals item shape,
// named once so the mapping below reads as a mapping. RestrictedRecord.Projects
// carries the identical shape, so one alias serves both lists.
type qualifyingRecordWire = struct {
	Id   openapi_types.UUID `json:"id"` //nolint:staticcheck // matches the generated RestrictedRecord.Deals item shape
	Name string             `json:"name"`
}

// qualifyingRecordsToWire maps one frozen-evidence list onto the wire.
func qualifyingRecordsToWire(records []QualifyingRecord) []qualifyingRecordWire {
	wire := make([]qualifyingRecordWire, 0, len(records))
	for _, record := range records {
		wire = append(wire, qualifyingRecordWire{Id: openapi_types.UUID(record.ID), Name: record.Name})
	}
	return wire
}

// restrictedRecordToWire states the obligation as class plus statute — never
// free text from a user — and names the deals and projects the evidence froze.
// The redacted-field list is always present, empty meaning "nothing was
// removed", which is a real state and not an unknown. The project list is
// always present too, for the same reason: an absent list and an empty one
// would read alike, and only one of them means "no project holds this".
func restrictedRecordToWire(record RestrictedRecord) crmcontracts.RestrictedRecord {
	deals := qualifyingRecordsToWire(record.Deals)
	projects := qualifyingRecordsToWire(record.Projects)
	redacted := record.RedactedFields
	if redacted == nil {
		redacted = []string{}
	}
	return crmcontracts.RestrictedRecord{
		ActivityId:      openapi_types.UUID(record.ActivityID),
		Kind:            record.Kind,
		OccurredAt:      record.OccurredAt,
		RestrictedAt:    record.RestrictedAt,
		RestrictedUntil: record.RestrictedUntil,
		Reason:          record.Class + " · " + statutoryBasisCorrespondence,
		Deals:           deals,
		Projects:        &projects,
		RedactedFields:  &redacted,
	}
}

// ReleaseRestrictedActivity implements (POST /retention/restrictions/{activityId}/release).
func (h Handlers) ReleaseRestrictedActivity(w http.ResponseWriter, r *http.Request, activityID openapi_types.UUID, _ crmcontracts.ReleaseRestrictedActivityParams) {
	reason, ok := decodeStatedReason(w, r)
	if !ok {
		return
	}
	if err := h.eraser.ReleaseRestriction(r.Context(), ids.UUID(activityID), reason); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PinActivityToFloor implements (POST /retention/restrictions/{activityId}/pin).
func (h Handlers) PinActivityToFloor(w http.ResponseWriter, r *http.Request, activityID openapi_types.UUID, _ crmcontracts.PinActivityToFloorParams) {
	reason, ok := decodeStatedReason(w, r)
	if !ok {
		return
	}
	if err := h.eraser.PinToFloor(r.Context(), ids.UUID(activityID), reason); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeStatedReason reads the one field both overrides carry and refuses an
// unstated one before any transaction opens. Shared because release and pin
// ask exactly the same thing of a caller, and two spellings of one refusal
// drift the first time either changes.
func decodeStatedReason(w http.ResponseWriter, r *http.Request) (StatedReason, bool) {
	var req crmcontracts.RetentionOverrideRequest
	if !httperr.Decode(w, r, &req) {
		return StatedReason{}, false
	}
	reason, err := ParseStatedReason(req.Reason)
	if err != nil {
		httperr.Write(w, r, err)
		return StatedReason{}, false
	}
	return reason, true
}
