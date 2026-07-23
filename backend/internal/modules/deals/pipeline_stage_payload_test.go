// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The pipeline/stage config family: drives pipelineCreatedPayload/
// stageCreatedPayload/stageUpdatedPayload — the exact functions
// CreatePipeline/CreateStage/UpdateStage call to build their
// pipeline.created/stage.created/stage.updated emits (pipeline.go,
// stages.go) — then round-trips the result through JSON exactly as
// storekit.EmitEvent marshals it into the outbox envelope's payload column.
// There is no non-integration harness in this repo that drives a Store
// method against a real Postgres (every such test lives under
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

func TestPipelineCreatedEmitsTypedPayload(t *testing.T) {
	stages := []StageInput{
		{Name: "New", Position: 0, Semantic: "open"},
		{Name: "Won", Position: 1, Semantic: "won"},
	}

	payload := pipelineCreatedPayload("Sales", true, stages)

	if !reflect.DeepEqual(payload.Name, "Sales") {
		t.Errorf("got %v, want %v", payload.Name, "Sales")
	}
	if !payload.IsDefault {
		t.Error("expected the condition to be true")
	}
	if len(payload.Stages) != 2 {
		t.Errorf("len = %d, want %d", len(payload.Stages), 2)
	}
	if !reflect.DeepEqual(payload.Stages[0].Name, "New") {
		t.Errorf("got %v, want %v", payload.Stages[0].Name, "New")
	}
	if !reflect.DeepEqual(payload.Stages[0].Position, 0) {
		t.Errorf("got %v, want %v", payload.Stages[0].Position, 0)
	}
	if !reflect.DeepEqual(payload.Stages[0].Semantic, "open") {
		t.Errorf("got %v, want %v", payload.Stages[0].Semantic, "open")
	}
	if !reflect.DeepEqual(payload.Stages[1].Name, "Won") {
		t.Errorf("got %v, want %v", payload.Stages[1].Name, "Won")
	}
	if !reflect.DeepEqual(payload.Stages[1].Position, 1) {
		t.Errorf("got %v, want %v", payload.Stages[1].Position, 1)
	}
	if !reflect.DeepEqual(payload.Stages[1].Semantic, "won") {
		t.Errorf("got %v, want %v", payload.Stages[1].Semantic, "won")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventPipelineCreated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestStageCreatedEmitsTypedPayload(t *testing.T) {
	pipelineID := ids.From[ids.PipelineKind](ids.NewV7())

	payload := stageCreatedPayload(pipelineID, "Negotiation", 2, "open", 40)

	if !reflect.DeepEqual(payload.PipelineId, openapi_types.UUID(pipelineID.UUID)) {
		t.Errorf("got %v, want %v", payload.PipelineId, openapi_types.UUID(pipelineID.UUID))
	}
	if !reflect.DeepEqual(payload.Name, "Negotiation") {
		t.Errorf("got %v, want %v", payload.Name, "Negotiation")
	}
	if !reflect.DeepEqual(payload.Position, 2) {
		t.Errorf("got %v, want %v", payload.Position, 2)
	}
	if !reflect.DeepEqual(payload.Semantic, "open") {
		t.Errorf("got %v, want %v", payload.Semantic, "open")
	}
	if !reflect.DeepEqual(payload.WinProbability, 40) {
		t.Errorf("got %v, want %v", payload.WinProbability, 40)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventStageCreated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestStageUpdatedEmitsTypedPayload(t *testing.T) {
	pipelineID := ids.From[ids.PipelineKind](ids.NewV7())
	newName := "Qualified"

	payload := stageUpdatedPayload(pipelineID, UpdateStageInput{Name: &newName})

	if !reflect.DeepEqual(payload.PipelineId, openapi_types.UUID(pipelineID.UUID)) {
		t.Errorf("got %v, want %v", payload.PipelineId, openapi_types.UUID(pipelineID.UUID))
	}
	if payload.Name == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.Name, newName) {
		t.Errorf("got %v, want %v", *payload.Name, newName)
	}
	if payload.Semantic != nil {
		t.Error("semantic must stay absent when the update did not touch it")
	}
	if payload.WinProbability != nil {
		t.Error("win_probability must stay absent when the update did not touch it")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The bounded delta is optional-fields, not an open map: an untouched
	// field must be OMITTED from the wire body, not marshaled as null —
	// a subscriber diffing keys would otherwise see a spurious change.
	if strings.Contains(string(raw), `"semantic"`) {
		t.Errorf("%q should not contain %q", string(raw), `"semantic"`)
	}
	if strings.Contains(string(raw), `"win_probability"`) {
		t.Errorf("%q should not contain %q", string(raw), `"win_probability"`)
	}

	var decoded crmcontracts.PublicEventStageUpdated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestStageUpdatedPayloadForcesTerminalWinProbability pins that the emitted
// stage.updated win_probability matches what the UPDATE committed: a terminal
// semantic forces won → 100 / lost → 0 in SQL, so the payload must carry that
// committed value, not the caller's input (which UPDATE ignored for a
// terminal semantic).
func TestStageUpdatedPayloadForcesTerminalWinProbability(t *testing.T) {
	pipelineID := ids.From[ids.PipelineKind](ids.NewV7())
	callerValue := 55 // what the caller sent; SQL ignores it for a terminal semantic

	for _, tc := range []struct {
		semantic string
		want     int
	}{
		{"won", 100},
		{"lost", 0},
	} {
		t.Run(tc.semantic, func(t *testing.T) {
			semantic := tc.semantic
			in := UpdateStageInput{Semantic: &semantic, WinProbability: &callerValue}
			payload := stageUpdatedPayload(pipelineID, in)
			if payload.WinProbability == nil {
				t.Fatalf("win_probability must be present for a terminal semantic")
			}
			if *payload.WinProbability != tc.want {
				t.Errorf("win_probability = %d, want %d (the committed terminal value)", *payload.WinProbability, tc.want)
			}
		})
	}

	// An open semantic leaves the caller's value untouched.
	open := "open"
	prob := 42
	payload := stageUpdatedPayload(pipelineID, UpdateStageInput{Semantic: &open, WinProbability: &prob})
	if payload.WinProbability == nil || *payload.WinProbability != 42 {
		t.Errorf("open semantic must keep the caller's win_probability 42, got %v", payload.WinProbability)
	}
}
