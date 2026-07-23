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
// Before this migration neither crmcontracts.PublicEventSignalDetected nor
// PublicEventSignalResolved existed, and both builder functions returned a
// map[string]any, so this test failed to compile (RED) until
// public-events.yaml gained the schemas, `make gen` regenerated the structs,
// and signal.go/resolver.go's builders were retyped.

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

var (
	signalPayloadTestSignalID = openapi_types.UUID(ids.MustParse("11111111-1111-1111-1111-111111111111"))
	signalPayloadTestOrgID    = openapi_types.UUID(ids.MustParse("22222222-2222-2222-2222-222222222222"))
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

	if !reflect.DeepEqual(payload.EventType(), "signal.detected") {
		t.Errorf("got %v, want %v", payload.EventType(), "signal.detected")
	}
	if !reflect.DeepEqual(payload.EntityType(), "signal") {
		t.Errorf("got %v, want %v", payload.EntityType(), "signal")
	}
	if !reflect.DeepEqual(payload.SignalId, signalPayloadTestSignalID) {
		t.Errorf("got %v, want %v", payload.SignalId, signalPayloadTestSignalID)
	}
	if !reflect.DeepEqual(payload.Kind, "stalled_deal") {
		t.Errorf("got %v, want %v", payload.Kind, "stalled_deal")
	}
	if !reflect.DeepEqual(payload.SourceChannel, "derived") {
		t.Errorf("got %v, want %v", payload.SourceChannel, "derived")
	}
	if !reflect.DeepEqual(payload.ResolutionState, "unresolved") {
		t.Errorf("got %v, want %v", payload.ResolutionState, "unresolved")
	}
	if !reflect.DeepEqual(payload.Severity, "info") {
		t.Errorf("got %v, want %v", payload.Severity, "info")
	}
	if payload.SubjectEntityType != nil {
		t.Errorf("expected nil, got %v", payload.SubjectEntityType)
	}
	if payload.SubjectEntityId != nil {
		t.Errorf("expected nil, got %v", payload.SubjectEntityId)
	}
	if payload.ResolutionConfidence != nil {
		t.Errorf("expected nil, got %v", payload.ResolutionConfidence)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), "entity_type") {
		t.Errorf("an absent entity_type must be omitted from the wire body, not marshaled as null: should not contain %v", "entity_type")
	}
	if strings.Contains(string(raw), "resolution_confidence") {
		t.Errorf("%q should not contain %q", string(raw), "resolution_confidence")
	}
	var decoded crmcontracts.PublicEventSignalDetected
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
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

	if payload.SubjectEntityType == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.SubjectEntityType, "organization") {
		t.Errorf("got %v, want %v", *payload.SubjectEntityType, "organization")
	}
	if payload.SubjectEntityId == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.SubjectEntityId, signalPayloadTestOrgID) {
		t.Errorf("got %v, want %v", *payload.SubjectEntityId, signalPayloadTestOrgID)
	}
	if payload.ResolutionConfidence == nil {
		t.Fatalf("expected non-nil value")
	}
	if math.Abs(float64(*payload.ResolutionConfidence)-float64(0.95)) > 0.0001 {
		t.Errorf("got %v, want %v +/- %v", *payload.ResolutionConfidence, 0.95, 0.0001)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventSignalDetected
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestResolvedPayload_Dropped proves the zero-candidate (dropped) shape: no
// resolved_org_id/resolved_person_id, no matched_on/match_confidence.
func TestResolvedPayload_Dropped(t *testing.T) {
	sig := crmcontracts.Signal{
		Id:              signalPayloadTestSignalID,
		ResolutionState: "dropped",
	}
	payload := resolvedPayload(sig, nil)

	if !reflect.DeepEqual(payload.EventType(), "signal.resolved") {
		t.Errorf("got %v, want %v", payload.EventType(), "signal.resolved")
	}
	if !reflect.DeepEqual(payload.EntityType(), "signal") {
		t.Errorf("got %v, want %v", payload.EntityType(), "signal")
	}
	if !reflect.DeepEqual(payload.SignalId, signalPayloadTestSignalID) {
		t.Errorf("got %v, want %v", payload.SignalId, signalPayloadTestSignalID)
	}
	if !reflect.DeepEqual(payload.ResolutionState, "dropped") {
		t.Errorf("got %v, want %v", payload.ResolutionState, "dropped")
	}
	if payload.ResolvedOrgId != nil {
		t.Errorf("expected nil, got %v", payload.ResolvedOrgId)
	}
	if payload.ResolvedPersonId != nil {
		t.Errorf("expected nil, got %v", payload.ResolvedPersonId)
	}
	if payload.MatchedOn != nil {
		t.Errorf("expected nil, got %v", payload.MatchedOn)
	}
	if payload.MatchConfidence != nil {
		t.Errorf("expected nil, got %v", payload.MatchConfidence)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), "matched_on") {
		t.Errorf("%q should not contain %q", string(raw), "matched_on")
	}
	var decoded crmcontracts.PublicEventSignalResolved
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
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

	if payload.ResolvedOrgId == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.ResolvedOrgId, signalPayloadTestOrgID) {
		t.Errorf("got %v, want %v", *payload.ResolvedOrgId, signalPayloadTestOrgID)
	}
	if payload.MatchedOn == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.MatchedOn, "domain") {
		t.Errorf("got %v, want %v", *payload.MatchedOn, "domain")
	}
	if payload.MatchConfidence == nil {
		t.Fatalf("expected non-nil value")
	}
	if math.Abs(float64(*payload.MatchConfidence)-float64(0.95)) > 0.0001 {
		t.Errorf("got %v, want %v +/- %v", *payload.MatchConfidence, 0.95, 0.0001)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventSignalResolved
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}
