// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The deal family: these tests drive dealStageChangedPayload — the exact
// function AdvanceDeal calls to build its deal.stage_changed emit
// (deal_advance.go) — against a real pre-move Deal snapshot, then round-trip
// the result through JSON exactly as storekit.Emit marshals it into the
// outbox envelope's payload column. There is no non-integration harness in
// this repo that drives a Store method against a real Postgres (every such
// test lives under compose/integration, gated `//go:build integration`,
// needing db-up); testing the production payload-construction function
// directly — the one place a schema/code mismatch would show up — is the
// honest substitute. The full DB-backed proof (a real HTTP advance, the real
// outbox row, validated against the published schema) already exists as
// compose/integration.TestDealStageChangedPayloadConformsToPublicSchema.

import (
	"encoding/json"
	"reflect"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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

	if payload.FromStageId == nil {
		t.Fatal("from_stage_id must carry the pre-move stage")
	}
	if !reflect.DeepEqual(*payload.FromStageId, fromStage) {
		t.Errorf("got %v, want %v", *payload.FromStageId, fromStage)
	}
	if !reflect.DeepEqual(payload.ToStageId, openapi_types.UUID(toStage.UUID)) {
		t.Errorf("got %v, want %v", payload.ToStageId, openapi_types.UUID(toStage.UUID))
	}
	if !reflect.DeepEqual(payload.FromStatus, "open") {
		t.Errorf("got %v, want %v", payload.FromStatus, "open")
	}
	if !reflect.DeepEqual(payload.ToStatus, "won") {
		t.Errorf("got %v, want %v", payload.ToStatus, "won")
	}
	if payload.AmountMinorAtChange == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.AmountMinorAtChange, amount) {
		t.Errorf("got %v, want %v", *payload.AmountMinorAtChange, amount)
	}
	if payload.CurrencyAtChange == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.CurrencyAtChange, currency) {
		t.Errorf("got %v, want %v", *payload.CurrencyAtChange, currency)
	}
	if !reflect.DeepEqual(payload.WinProbability, 100) {
		t.Errorf("got %v, want %v", payload.WinProbability, 100)
	}

	// Round-trip through JSON exactly as storekit.Emit marshals the payload
	// into the outbox envelope — the wire shape the pilot's conformance and
	// snapshot gates both check.
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventDealStageChanged
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
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
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	got, ok := decoded["to_status"]
	if !ok {
		t.Errorf(`expected JSON key "to_status" (bound in automation/handlers_event.go's dealStageStatusField) in %s`, raw)
	}
	if !reflect.DeepEqual(got, "lost") {
		t.Errorf("got %v, want %v", got, "lost")
	}
}
