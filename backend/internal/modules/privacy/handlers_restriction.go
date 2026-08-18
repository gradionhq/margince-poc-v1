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

// qualifyingDealWire is the generated RestrictedRecord.Deals item shape,
// named once so the mapping below reads as a mapping.
type qualifyingDealWire = struct {
	Id   openapi_types.UUID `json:"id"` //nolint:staticcheck // matches the generated RestrictedRecord.Deals item shape
	Name string             `json:"name"`
}

// restrictedRecordToWire states the obligation as class plus statute — never
// free text from a user — and names the deals the evidence froze. The
// redacted-field list is always present, empty meaning "nothing was removed",
// which is a real state and not an unknown.
func restrictedRecordToWire(record RestrictedRecord) crmcontracts.RestrictedRecord {
	deals := make([]qualifyingDealWire, 0, len(record.Deals))
	for _, deal := range record.Deals {
		deals = append(deals, qualifyingDealWire{Id: openapi_types.UUID(deal.ID), Name: deal.Name})
	}
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
		RedactedFields:  &redacted,
	}
}
