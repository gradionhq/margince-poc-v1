// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The two events a lead promotion emits, built here so promote.go holds the
// transaction and this file holds the wire shapes.

import (
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// promotedPersonPayload builds the person-side event a lead promotion
// emits — its own verb (person.created) on a fresh person, or a
// person.updated changed_fields note carrying the fields the merge ACTUALLY
// applied when the promotion instead merged into an existing person
// (merged=true, PO-F-1). changed_fields is the real merge delta (mergeFields),
// so it reports a filled title and omits converted_from_lead_id when that was
// already set — not a fixed map that could misstate the change. A merge that
// applied nothing returns nil (no person.updated to emit). The two shapes are
// different published events, not variants of one, so the return type is the
// shared events.Payload seam rather than a single struct.
//
//nolint:ireturn // dispatches to PublicEventPersonCreated vs Updated by the merged condition; tested directly via the interface in person_organization_payload_test.go
func promotedPersonPayload(person crmcontracts.Person, merged bool, mergeFields map[string]any) events.Payload {
	if merged {
		if len(mergeFields) == 0 {
			return nil
		}
		return crmcontracts.PublicEventPersonUpdated{ChangedFields: mergeFields}
	}
	return crmcontracts.PublicEventPersonCreated{FullName: person.FullName}
}

// leadPromotedPayload builds the lead-side event a promotion emits —
// its own verb (events.md §5.5), never a lead.updated. evidenceActivityID
// is nil for a human_qualify with no linked activity; the wire field is
// then omitted rather than marshaled as null.
func leadPromotedPayload(personID ids.PersonID, outcome, trigger string, evidenceActivityID *ids.ActivityID) crmcontracts.PublicEventLeadPromoted {
	p := crmcontracts.PublicEventLeadPromoted{
		PromotedPersonId: openapi_types.UUID(personID.UUID),
		DedupeOutcome:    outcome,
		Trigger:          trigger,
	}
	if evidenceActivityID != nil {
		p.EvidenceRef = uuidPtr(&evidenceActivityID.UUID)
	}
	return p
}
