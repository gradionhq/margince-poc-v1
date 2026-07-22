// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package signals

// TDD Step 1 of the webhooks Task 5e migration (signals family): drives the
// payload-builder functions this package's emit sites call — detectedPayload
// (signal.go's CreateSignal) and resolvedPayload (resolver.go's resolveTx) —
// then round-trips each result through JSON exactly as storekit.EmitEvent
// marshals it into the outbox envelope's payload column. There is no
// non-integration harness in this repo that drives a Store method against a
// real Postgres (every such test lives under compose/integration, gated
// `//go:build integration`, needing db-up); testing the production
// payload-construction functions directly — the one place a schema/code
// mismatch would show up — is the honest substitute, mirroring the activity
// family's TestActivityCapturedPayload_DirectLog (webhooks Task 5c).
//
// Before this migration neither crmcontracts.WebhookPayloadSignalDetected nor
// WebhookPayloadSignalResolved existed, and both builder functions returned a
// map[string]any, so this test failed to compile (RED) until
// public-events.yaml gained the schemas, `make gen` regenerated the structs,
// and signal.go/resolver.go's builders were retyped.

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/stretchr/testify/require"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var (
	signalPayloadTestSignalID = openapi_types.UUID(uuid.MustParse("11111111-1111-1111-1111-111111111111"))
	signalPayloadTestOrgID    = openapi_types.UUID(uuid.MustParse("22222222-2222-2222-2222-222222222222"))
)

// TestDetectedPayload_Unresolved proves the raw (no subject yet) shape: no
// entity_type/entity_id, no resolution_confidence.
func TestDetectedPayload_Unresolved(t *testing.T) {
	sig := crmcontracts.Signal{
		Id:              signalPayloadTestSignalID,
		Kind:            "stalled_deal",
		SourceChannel:   "derived",
		ResolutionState: "unresolved",
		Severity:        "info",
	}
	payload := detectedPayload(sig)

	require.Equal(t, "signal.detected", payload.EventType())
	require.Equal(t, "signal", payload.EntityType())
	require.Equal(t, signalPayloadTestSignalID, payload.SignalId)
	require.Equal(t, "stalled_deal", payload.Kind)
	require.Equal(t, "derived", payload.SourceChannel)
	require.Equal(t, "unresolved", payload.ResolutionState)
	require.Equal(t, "info", payload.Severity)
	require.Nil(t, payload.SubjectEntityType)
	require.Nil(t, payload.SubjectEntityId)
	require.Nil(t, payload.ResolutionConfidence)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "entity_type",
		"an absent entity_type must be omitted from the wire body, not marshaled as null")
	require.NotContains(t, string(raw), "resolution_confidence")
	var decoded crmcontracts.WebhookPayloadSignalDetected
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestDetectedPayload_WithSubject proves a signal created ABOUT a known
// record carries entity_type/entity_id (created already resolved).
func TestDetectedPayload_WithSubject(t *testing.T) {
	entityType := crmcontracts.SignalEntityType("organization")
	confidence := float32(0.95)
	sig := crmcontracts.Signal{
		Id:                   signalPayloadTestSignalID,
		Kind:                 "champion_left",
		SourceChannel:        "inbound",
		ResolutionState:      "resolved",
		Severity:             "warn",
		EntityType:           &entityType,
		EntityId:             &signalPayloadTestOrgID,
		ResolutionConfidence: &confidence,
	}
	payload := detectedPayload(sig)

	require.NotNil(t, payload.SubjectEntityType)
	require.Equal(t, "organization", *payload.SubjectEntityType)
	require.NotNil(t, payload.SubjectEntityId)
	require.Equal(t, signalPayloadTestOrgID, *payload.SubjectEntityId)
	require.NotNil(t, payload.ResolutionConfidence)
	require.InDelta(t, 0.95, *payload.ResolutionConfidence, 0.0001)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadSignalDetected
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestResolvedPayload_Dropped proves the zero-candidate (dropped) shape: no
// resolved_org_id/resolved_person_id, no matched_on/match_confidence.
func TestResolvedPayload_Dropped(t *testing.T) {
	sig := crmcontracts.Signal{
		Id:              signalPayloadTestSignalID,
		ResolutionState: "dropped",
	}
	payload := resolvedPayload(sig, nil)

	require.Equal(t, "signal.resolved", payload.EventType())
	require.Equal(t, "signal", payload.EntityType())
	require.Equal(t, signalPayloadTestSignalID, payload.SignalId)
	require.Equal(t, "dropped", payload.ResolutionState)
	require.Nil(t, payload.ResolvedOrgId)
	require.Nil(t, payload.ResolvedPersonId)
	require.Nil(t, payload.MatchedOn)
	require.Nil(t, payload.MatchConfidence)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "matched_on")
	var decoded crmcontracts.WebhookPayloadSignalResolved
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestResolvedPayload_ResolvedToOrg proves the single-candidate (resolved)
// shape: resolved_org_id, matched_on/match_confidence all set.
func TestResolvedPayload_ResolvedToOrg(t *testing.T) {
	orgID := ids.From[ids.OrganizationKind](ids.UUID(signalPayloadTestOrgID))
	sig := crmcontracts.Signal{
		Id:              signalPayloadTestSignalID,
		ResolutionState: "resolved",
		ResolvedOrgId:   &signalPayloadTestOrgID,
	}
	candidates := []candidate{{OrgID: orgID, MatchedOn: "domain", Confidence: 0.95}}
	payload := resolvedPayload(sig, candidates)

	require.NotNil(t, payload.ResolvedOrgId)
	require.Equal(t, signalPayloadTestOrgID, *payload.ResolvedOrgId)
	require.NotNil(t, payload.MatchedOn)
	require.Equal(t, "domain", *payload.MatchedOn)
	require.NotNil(t, payload.MatchConfidence)
	require.InDelta(t, 0.95, *payload.MatchConfidence, 0.0001)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadSignalResolved
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}
