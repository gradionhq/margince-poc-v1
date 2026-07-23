// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// TDD Step 1 of the webhooks Task 5b-lead migration (lead family): drives
// leadCreatedCapturePayload — the exact function captureLead calls to
// build its lead.created emit for a fresh auto-created lead (sink.go) —
// then round-trips the result through JSON exactly as storekit.EmitEvent
// marshals it into the outbox envelope's payload column. This is the
// second of lead.created's two emit sites (EMIT-INVENTORY.md);
// people/lead.go's direct-create site sets no fields at all and needs no
// builder.
//
// Before this migration crmcontracts.PublicEventLeadCreated and
// leadCreatedCapturePayload did not exist, so this test failed to
// compile (RED) until public-events.yaml gained the schema, `make gen`
// regenerated the struct, and sink.go grew the builder.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestLeadCreatedCapturePayload(t *testing.T) {
	payload := leadCreatedCapturePayload("linkedin")

	require.Equal(t, "lead.created", payload.EventType())
	require.Equal(t, "lead", payload.EntityType())
	require.NotNil(t, payload.SourceSystem)
	require.Equal(t, "linkedin", *payload.SourceSystem)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventLeadCreated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}
