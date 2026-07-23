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
	"reflect"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestLeadCreatedCapturePayload(t *testing.T) {
	payload := leadCreatedCapturePayload("linkedin")

	if !reflect.DeepEqual(payload.EventType(), "lead.created") {
		t.Errorf("got %v, want %v", payload.EventType(), "lead.created")
	}
	if !reflect.DeepEqual(payload.EntityType(), "lead") {
		t.Errorf("got %v, want %v", payload.EntityType(), "lead")
	}
	if payload.SourceSystem == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.SourceSystem, "linkedin") {
		t.Errorf("got %v, want %v", *payload.SourceSystem, "linkedin")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventLeadCreated
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}
