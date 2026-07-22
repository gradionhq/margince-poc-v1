// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// TDD Step 1 of the webhooks Task 5a-iii migration (pipeline/stage config
// family): drives pipelineCreatedPayload/stageCreatedPayload/
// stageUpdatedPayload — the exact functions CreatePipeline/CreateStage/
// UpdateStage call to build their pipeline.created/stage.created/
// stage.updated emits (pipeline.go, stages.go) — then round-trips the
// result through JSON exactly as storekit.EmitEvent marshals it into the
// outbox envelope's payload column. There is no non-integration harness in
// this repo that drives a Store method against a real Postgres (every such
// test lives under compose/integration, gated `//go:build integration`,
// needing db-up); testing the production payload-construction functions
// directly — the one place a schema/code mismatch would show up — is the
// honest substitute, mirroring the deal family's
// TestDealStageChangedEmitsTypedPayload (webhooks Task 5a-i) and the offer
// family's TestOfferSentEmitsTypedPayload (Task 5a-ii).
//
// Before this migration crmcontracts.WebhookPayloadPipelineCreated/
// StageCreated/StageUpdated did not exist and none of the three builder
// functions existed, so this test failed to compile (RED) until
// public-events.yaml gained the schemas, `make gen` regenerated the
// structs, and pipeline.go/stages.go grew the builders.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

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

	require.Equal(t, "Sales", payload.Name)
	require.True(t, payload.IsDefault)
	require.Len(t, payload.Stages, 2)
	require.Equal(t, "New", payload.Stages[0].Name)
	require.Equal(t, 0, payload.Stages[0].Position)
	require.Equal(t, "open", payload.Stages[0].Semantic)
	require.Equal(t, "Won", payload.Stages[1].Name)
	require.Equal(t, 1, payload.Stages[1].Position)
	require.Equal(t, "won", payload.Stages[1].Semantic)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadPipelineCreated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestStageCreatedEmitsTypedPayload(t *testing.T) {
	pipelineID := ids.From[ids.PipelineKind](ids.NewV7())

	payload := stageCreatedPayload(pipelineID, "Negotiation", 2, "open", 40)

	require.Equal(t, openapi_types.UUID(pipelineID.UUID), payload.PipelineId)
	require.Equal(t, "Negotiation", payload.Name)
	require.Equal(t, 2, payload.Position)
	require.Equal(t, "open", payload.Semantic)
	require.Equal(t, 40, payload.WinProbability)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadStageCreated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestStageUpdatedEmitsTypedPayload(t *testing.T) {
	pipelineID := ids.From[ids.PipelineKind](ids.NewV7())
	newName := "Qualified"

	payload := stageUpdatedPayload(pipelineID, UpdateStageInput{Name: &newName})

	require.Equal(t, openapi_types.UUID(pipelineID.UUID), payload.PipelineId)
	require.NotNil(t, payload.Name)
	require.Equal(t, newName, *payload.Name)
	require.Nil(t, payload.Semantic, "semantic must stay absent when the update did not touch it")
	require.Nil(t, payload.WinProbability, "win_probability must stay absent when the update did not touch it")

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	// The bounded delta is optional-fields, not an open map: an untouched
	// field must be OMITTED from the wire body, not marshaled as null —
	// a subscriber diffing keys would otherwise see a spurious change.
	require.NotContains(t, string(raw), `"semantic"`)
	require.NotContains(t, string(raw), `"win_probability"`)

	var decoded crmcontracts.WebhookPayloadStageUpdated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}
