// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// TDD Step 1 of the webhooks Task 5a-ii migration (offer family): drives
// offerSentPayload — the exact function SendOffer calls to build its
// offer.sent emit (offer_lifecycle.go) — against a real pre-send Offer
// snapshot, then round-trips the result through JSON exactly as
// storekit.Emit marshals it into the outbox envelope's payload column.
// There is no non-integration harness in this repo that drives a Store
// method against a real Postgres (every such test lives under
// compose/integration, gated `//go:build integration`, needing db-up);
// testing the production payload-construction function directly — the one
// place a schema/code mismatch would show up — is the honest substitute,
// mirroring the deal family's TestDealStageChangedEmitsTypedPayload
// (webhooks Task 5a-i).
//
// Before the offer family migration crmcontracts.WebhookPayloadOfferSent
// did not exist and offerSentPayload did not exist, so this test failed to
// compile (RED) until public-events.yaml gained the schema, `make gen`
// regenerated the struct, and offer_lifecycle.go grew the builder.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

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

	require.Equal(t, offerID, payload.OfferId)
	require.Equal(t, dealID, payload.DealId)
	require.NotNil(t, payload.Revision)
	require.Equal(t, revision, *payload.Revision)
	require.NotNil(t, payload.GrossMinor)
	require.Equal(t, gross, *payload.GrossMinor)
	require.Equal(t, "1.0842", payload.FxRateToBase)
	require.Equal(t, &validUntil, payload.ValidUntil)

	// Round-trip through JSON exactly as storekit.Emit marshals the
	// payload into the outbox envelope — the wire shape the offer
	// snapshot gate checks.
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadOfferSent
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}
