// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The lead family: drives leadPromotedPayload — the exact function
// FinalizeLeadPromotion's promotion path calls to build its lead.promoted
// emit (promote.go) — then round-trips the result through JSON exactly as
// storekit.EmitEvent marshals it into the outbox envelope's payload column.
// It also proves the OPEN lead.updated envelope's changed_fields map
// preserves a runtime cf_* custom-field key verbatim, since that is exactly
// why lead.updated is modeled as an open map rather than a strictly typed
// struct. There is no non-integration harness in this repo that drives a
// Store method against a real Postgres (every such test lives under
// compose/integration, gated `//go:build integration`, needing db-up);
// testing the production payload-construction functions directly — the one
// place a schema/code mismatch would show up — is the honest substitute.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestLeadPromotedPayload_WithEvidence(t *testing.T) {
	personID := ids.From[ids.PersonKind](ids.NewV7())
	evidenceID := ids.From[ids.ActivityKind](ids.NewV7())

	payload := leadPromotedPayload(personID, "created", "inbound_reply", &evidenceID)

	if payload.EventType() != "lead.promoted" {
		t.Errorf("got %v, want %v", payload.EventType(), "lead.promoted")
	}
	if payload.EntityType() != "lead" {
		t.Errorf("got %v, want %v", payload.EntityType(), "lead")
	}
	if payload.PromotedPersonId != openapi_types.UUID(personID.UUID) {
		t.Errorf("got %v, want %v", payload.PromotedPersonId, openapi_types.UUID(personID.UUID))
	}
	if payload.DedupeOutcome != "created" {
		t.Errorf("got %v, want %v", payload.DedupeOutcome, "created")
	}
	if payload.Trigger != "inbound_reply" {
		t.Errorf("got %v, want %v", payload.Trigger, "inbound_reply")
	}
	if payload.EvidenceRef == nil {
		t.Fatalf("expected non-nil value")
	}
	if *payload.EvidenceRef != openapi_types.UUID(evidenceID.UUID) {
		t.Errorf("got %v, want %v", *payload.EvidenceRef, openapi_types.UUID(evidenceID.UUID))
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventLeadPromoted
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestLeadPromotedPayload_MergedNoEvidence(t *testing.T) {
	personID := ids.From[ids.PersonKind](ids.NewV7())

	payload := leadPromotedPayload(personID, "merged", "human_qualify", nil)

	if payload.DedupeOutcome != "merged" {
		t.Errorf("got %v, want %v", payload.DedupeOutcome, "merged")
	}
	if payload.EvidenceRef != nil {
		t.Errorf("expected nil, got %v", payload.EvidenceRef)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), `"evidence_ref"`) {
		t.Errorf("an absent evidence_ref must be omitted from the wire body, not marshaled as null: should not contain %v", `"evidence_ref"`)
	}
}

// TestLeadUpdatedChangedFieldsPreservesCustomField proves the OPEN
// lead.updated envelope's changed_fields map round-trips a runtime cf_*
// custom-field key verbatim — the honest reason lead.updated is an open
// map rather than a strictly typed struct.
func TestLeadUpdatedChangedFieldsPreservesCustomField(t *testing.T) {
	payload := crmcontracts.PublicEventLeadUpdated{
		ChangedFields: map[string]any{
			"score":              float64(72),
			"cf_lead_source_ref": "partner-9f2",
		},
	}

	if payload.EventType() != "lead.updated" {
		t.Errorf("got %v, want %v", payload.EventType(), "lead.updated")
	}
	if payload.EntityType() != "lead" {
		t.Errorf("got %v, want %v", payload.EntityType(), "lead")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventLeadUpdated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if decoded.ChangedFields["cf_lead_source_ref"] != "partner-9f2" {
		t.Errorf("the open changed_fields map must preserve a cf_* custom-field key untouched: got %v, want %v", decoded.ChangedFields["cf_lead_source_ref"], "partner-9f2")
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}
