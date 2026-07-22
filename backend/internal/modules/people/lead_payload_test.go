// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// TDD Step 1 of the webhooks Task 5b-lead migration (lead family): drives
// leadPromotedPayload — the exact function FinalizeLeadPromotion's
// promotion path calls to build its lead.promoted emit (promote.go) —
// then round-trips the result through JSON exactly as storekit.EmitEvent
// marshals it into the outbox envelope's payload column. It also proves
// the OPEN lead.updated envelope's changed_fields map preserves a
// runtime cf_* custom-field key verbatim, since that is exactly why
// lead.updated is modeled as an open map rather than a strictly typed
// struct (EMIT-INVENTORY.md). There is no non-integration harness in
// this repo that drives a Store method against a real Postgres (every
// such test lives under compose/integration, gated `//go:build
// integration`, needing db-up); testing the production
// payload-construction functions directly — the one place a
// schema/code mismatch would show up — is the honest substitute,
// mirroring the person/organization family's
// TestPromotedPersonPayload_Created (webhooks Task 5b-personorg).
//
// Before this migration crmcontracts.WebhookPayloadLeadCreated/
// Disqualified/Promoted/Updated did not exist and leadPromotedPayload
// did not exist, so this test failed to compile (RED) until
// public-events.yaml gained the schemas, `make gen` regenerated the
// structs, and promote.go grew the builder.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestLeadPromotedPayload_WithEvidence(t *testing.T) {
	personID := ids.From[ids.PersonKind](ids.NewV7())
	evidenceID := ids.From[ids.ActivityKind](ids.NewV7())

	payload := leadPromotedPayload(personID, "created", "inbound_reply", &evidenceID)

	require.Equal(t, "lead.promoted", payload.EventType())
	require.Equal(t, "lead", payload.EntityType())
	require.Equal(t, openapi_types.UUID(personID.UUID), payload.PromotedPersonId)
	require.Equal(t, "created", payload.DedupeOutcome)
	require.Equal(t, "inbound_reply", payload.Trigger)
	require.NotNil(t, payload.EvidenceRef)
	require.Equal(t, openapi_types.UUID(evidenceID.UUID), *payload.EvidenceRef)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadLeadPromoted
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestLeadPromotedPayload_MergedNoEvidence(t *testing.T) {
	personID := ids.From[ids.PersonKind](ids.NewV7())

	payload := leadPromotedPayload(personID, "merged", "human_qualify", nil)

	require.Equal(t, "merged", payload.DedupeOutcome)
	require.Nil(t, payload.EvidenceRef)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"evidence_ref"`,
		"an absent evidence_ref must be omitted from the wire body, not marshaled as null")
}

// TestLeadUpdatedChangedFieldsPreservesCustomField proves the OPEN
// lead.updated envelope's changed_fields map round-trips a runtime cf_*
// custom-field key verbatim — the honest reason lead.updated is an open
// map rather than a strictly typed struct (EMIT-INVENTORY.md).
func TestLeadUpdatedChangedFieldsPreservesCustomField(t *testing.T) {
	payload := crmcontracts.WebhookPayloadLeadUpdated{
		ChangedFields: map[string]any{
			"score":              float64(72),
			"cf_lead_source_ref": "partner-9f2",
		},
	}

	require.Equal(t, "lead.updated", payload.EventType())
	require.Equal(t, "lead", payload.EntityType())

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadLeadUpdated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, "partner-9f2", decoded.ChangedFields["cf_lead_source_ref"],
		"the open changed_fields map must preserve a cf_* custom-field key untouched")
	require.Equal(t, payload, decoded)
}
