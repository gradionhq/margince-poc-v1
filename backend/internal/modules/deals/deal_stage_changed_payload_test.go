// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// TDD Step 1 of the webhooks Task 5a-i pilot migration (deal family): these
// tests drive dealStageChangedPayload — the exact function AdvanceDeal calls
// to build its deal.stage_changed emit (deal_advance.go) — against a real
// pre-move Deal snapshot, then round-trip the result through JSON exactly as
// storekit.Emit marshals it into the outbox envelope's payload column. There
// is no non-integration harness in this repo that drives a Store method
// against a real Postgres (every such test lives under compose/integration,
// gated `//go:build integration`, needing db-up); testing the production
// payload-construction function directly — the one place a schema/code
// mismatch would show up — is the honest substitute. The full DB-backed
// proof (a real HTTP advance, the real outbox row, validated against the
// published schema) already exists as
// compose/integration.TestDealStageChangedPayloadConformsToPublicSchema.
//
// Before the deal family migration these fields (from_status, to_status,
// amount_minor_at_change, currency_at_change, win_probability) did not exist
// on crmcontracts.PublicEventDealStageChanged — the pilot schema only
// carried deal_id/pipeline_id/from_stage_id/to_stage_id — so this test
// failed to compile (RED) until public-events.yaml was reconciled to the
// actual emit-site fields and `make gen` regenerated the struct.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestDealStageChangedEmitsTypedPayload(t *testing.T) {
	fromStage := openapi_types.UUID(ids.NewV7())
	toStage := ids.From[ids.StageKind](ids.NewV7())
	amount := int64(250000)
	currency := "EUR"
	current := crmcontracts.Deal{
		StageId:     &fromStage,
		Status:      crmcontracts.DealStatus(DealOpen),
		AmountMinor: &amount,
		Currency:    &currency,
	}

	payload := dealStageChangedPayload(current, toStage, string(DealWon), 100)

	require.NotNil(t, payload.FromStageId, "from_stage_id must carry the pre-move stage")
	require.Equal(t, fromStage, *payload.FromStageId)
	require.Equal(t, openapi_types.UUID(toStage.UUID), payload.ToStageId)
	require.Equal(t, "open", payload.FromStatus)
	require.Equal(t, "won", payload.ToStatus)
	require.NotNil(t, payload.AmountMinorAtChange)
	require.Equal(t, amount, *payload.AmountMinorAtChange)
	require.NotNil(t, payload.CurrencyAtChange)
	require.Equal(t, currency, *payload.CurrencyAtChange)
	require.Equal(t, 100, payload.WinProbability)

	// Round-trip through JSON exactly as storekit.Emit marshals the payload
	// into the outbox envelope — the wire shape the pilot's conformance and
	// snapshot gates both check.
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventDealStageChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestDealStageChangedToStatusJSONTagIsStable is the A9 key-binding
// regression test: automation/handlers_event.go's dealStageStatusField
// decodes deal.stage_changed's outbox payload by the literal JSON key
// "to_status" (automation cannot import the deals or contracts payload
// type — it reads the generic map[string]any the bus hands every
// subscriber). A future rename of this schema field must break THIS test,
// not silently stop the automation trigger from matching.
func TestDealStageChangedToStatusJSONTagIsStable(t *testing.T) {
	payload := crmcontracts.PublicEventDealStageChanged{
		ToStageId:      openapi_types.UUID(ids.NewV7()),
		FromStatus:     "open",
		ToStatus:       "lost",
		WinProbability: 0,
	}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	got, ok := decoded["to_status"]
	require.True(t, ok, `expected JSON key "to_status" (bound in automation/handlers_event.go's dealStageStatusField) in %s`, raw)
	require.Equal(t, "lost", got)
}
