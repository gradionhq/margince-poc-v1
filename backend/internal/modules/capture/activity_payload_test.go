// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The activities family, capture's two emit sites: drives
// activityCaptureEventPayload (captureActivity's activity.captured emit)
// and engagementReplyPayload (emitReply's engagement.reply emit), then
// round-trips each result through JSON exactly as storekit.EmitEvent
// marshals it into the outbox envelope's payload column — the one place a
// schema/code mismatch would show up, since no non-integration harness
// drives a Store method against a real Postgres.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestActivityCaptureEventPayload proves the capture auto-create path names
// its originating source system — the field the direct-log path
// (activities/activity.go) never sets.
func TestActivityCaptureEventPayload(t *testing.T) {
	payload := activityCaptureEventPayload("email", "gmail")

	if !reflect.DeepEqual(payload.EventType(), "activity.captured") {
		t.Errorf("got %v, want %v", payload.EventType(), "activity.captured")
	}
	if !reflect.DeepEqual(payload.EntityType(), "activity") {
		t.Errorf("got %v, want %v", payload.EntityType(), "activity")
	}
	if !reflect.DeepEqual(payload.Kind, "email") {
		t.Errorf("got %v, want %v", payload.Kind, "email")
	}
	if payload.SourceSystem == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.SourceSystem, "gmail") {
		t.Errorf("got %v, want %v", *payload.SourceSystem, "gmail")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventActivityCaptured
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestEngagementReplyPayload_WithContact proves the reply payload carries
// the resolved contact_id when the counterparty is already a known person.
func TestEngagementReplyPayload_WithContact(t *testing.T) {
	matched := ids.NewV7()
	occurredAt := time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC)
	contact := ids.From[ids.PersonKind](ids.NewV7())

	payload := engagementReplyPayload(matched, occurredAt, "gmail:msg-42", &contact)

	if !reflect.DeepEqual(payload.EventType(), "engagement.reply") {
		t.Errorf("got %v, want %v", payload.EventType(), "engagement.reply")
	}
	if !reflect.DeepEqual(payload.EntityType(), "activity") {
		t.Errorf("got %v, want %v", payload.EntityType(), "activity")
	}
	if !reflect.DeepEqual(payload.MatchedOutboundActivityId, openapi_types.UUID(matched)) {
		t.Errorf("got %v, want %v", payload.MatchedOutboundActivityId, openapi_types.UUID(matched))
	}
	if !reflect.DeepEqual(payload.Channel, "email") {
		t.Errorf("got %v, want %v", payload.Channel, "email")
	}
	if !reflect.DeepEqual(payload.OccurredAt, occurredAt) {
		t.Errorf("got %v, want %v", payload.OccurredAt, occurredAt)
	}
	if !reflect.DeepEqual(payload.IdempotencyKey, "gmail:msg-42") {
		t.Errorf("got %v, want %v", payload.IdempotencyKey, "gmail:msg-42")
	}
	if payload.ContactId == nil {
		t.Fatalf("expected non-nil value")
	}
	if !reflect.DeepEqual(*payload.ContactId, openapi_types.UUID(contact.UUID)) {
		t.Errorf("got %v, want %v", *payload.ContactId, openapi_types.UUID(contact.UUID))
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventEngagementReply
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

// TestEngagementReplyPayload_NoContact proves an unresolved counterparty
// omits contact_id from the wire body rather than marshaling it as null.
func TestEngagementReplyPayload_NoContact(t *testing.T) {
	matched := ids.NewV7()
	occurredAt := time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC)

	payload := engagementReplyPayload(matched, occurredAt, "gmail:msg-43", nil)

	if payload.ContactId != nil {
		t.Errorf("expected nil, got %v", payload.ContactId)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(raw), "contact_id") {
		t.Errorf("%q should not contain %q", string(raw), "contact_id")
	}
}
