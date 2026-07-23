// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The offer family: drives offerSentPayload — the exact function SendOffer
// calls to build its offer.sent emit (offer_lifecycle.go) — against a real
// pre-send Offer snapshot, then round-trips the result through JSON exactly
// as storekit.Emit marshals it into the outbox envelope's payload column.
// There is no non-integration harness in this repo that drives a Store
// method against a real Postgres (every such test lives under
// compose/integration, gated `//go:build integration`, needing db-up);
// testing the production payload-construction function directly — the one
// place a schema/code mismatch would show up — is the honest substitute.

import (
	"encoding/json"
	"reflect"
	"testing"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestOfferSentEmitsTypedPayload(t *testing.T) {
	offerID := openapi_types.UUID(ids.NewV7())
	dealID := openapi_types.UUID(ids.NewV7())
	revision := 2
	gross := int64(500000)
	validUntil := openapi_types.Date{}
	current := crmcontracts.Offer{
		Id:         offerID,
		DealId:     dealID,
		Revision:   &revision,
		GrossMinor: &gross,
		ValidUntil: &validUntil,
	}

	payload := offerSentPayload(current, "1.0842")

	if !reflect.DeepEqual(payload.OfferId, offerID) {
		t.Errorf("got %v, want %v", payload.OfferId, offerID)
	}
	if !reflect.DeepEqual(payload.DealId, dealID) {
		t.Errorf("got %v, want %v", payload.DealId, dealID)
	}
	if payload.Revision == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.Revision, revision) {
		t.Errorf("got %v, want %v", *payload.Revision, revision)
	}
	if payload.GrossMinor == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.GrossMinor, gross) {
		t.Errorf("got %v, want %v", *payload.GrossMinor, gross)
	}
	if !reflect.DeepEqual(payload.FxRateToBase, "1.0842") {
		t.Errorf("got %v, want %v", payload.FxRateToBase, "1.0842")
	}
	if !reflect.DeepEqual(payload.ValidUntil, &validUntil) {
		t.Errorf("got %v, want %v", payload.ValidUntil, &validUntil)
	}

	// Round-trip through JSON exactly as storekit.Emit marshals the
	// payload into the outbox envelope — the wire shape the offer
	// snapshot gate checks.
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventOfferSent
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}
