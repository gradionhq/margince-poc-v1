// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The lead family, capture's emit site: drives leadCreatedCapturePayload —
// the exact function captureLead calls to build its lead.created emit for a
// fresh auto-created lead (sink.go) — then round-trips the result through
// JSON exactly as storekit.EmitEvent marshals it into the outbox envelope's
// payload column. This is the second of lead.created's two emit sites;
// people/lead.go's direct-create site sets no fields at all and needs no
// builder.

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
