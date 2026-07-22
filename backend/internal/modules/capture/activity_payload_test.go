// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// TDD Step 1 of the webhooks Task 5c migration (activities family,
// capture's two emit sites): drives activityCaptureEventPayload
// (captureActivity's activity.captured emit) and engagementReplyPayload
// (emitReply's engagement.reply emit), then round-trips each result
// through JSON exactly as storekit.EmitEvent marshals it into the outbox
// envelope's payload column, mirroring the lead family's
// TestLeadPromotedPayload_WithEvidence (webhooks Task 5b-lead).
//
// Before this migration crmcontracts.WebhookPayloadActivityCaptured/
// EngagementReply did not exist, and neither builder existed, so this test
// failed to compile (RED) until public-events.yaml gained the schemas,
// `make gen` regenerated the structs, and sink.go grew the builders.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestActivityCaptureEventPayload proves the capture auto-create path names
// its originating source system — the field the direct-log path
// (activities/activity.go) never sets.
func TestActivityCaptureEventPayload(t *testing.T) {
	payload := activityCaptureEventPayload("email", "gmail")

	require.Equal(t, "activity.captured", payload.EventType())
	require.Equal(t, "activity", payload.EntityType())
	require.Equal(t, "email", payload.Kind)
	require.NotNil(t, payload.SourceSystem)
	require.Equal(t, "gmail", *payload.SourceSystem)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadActivityCaptured
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestEngagementReplyPayload_WithContact proves the reply payload carries
// the resolved contact_id when the counterparty is already a known person.
func TestEngagementReplyPayload_WithContact(t *testing.T) {
	matched := ids.NewV7()
	occurredAt := time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC)
	contact := ids.From[ids.PersonKind](ids.NewV7())

	payload := engagementReplyPayload(matched, occurredAt, "gmail:msg-42", &contact)

	require.Equal(t, "engagement.reply", payload.EventType())
	require.Equal(t, "activity", payload.EntityType())
	require.Equal(t, openapi_types.UUID(matched), payload.MatchedOutboundActivityId)
	require.Equal(t, "email", payload.Channel)
	require.Equal(t, occurredAt, payload.OccurredAt)
	require.Equal(t, "gmail:msg-42", payload.IdempotencyKey)
	require.NotNil(t, payload.ContactId)
	require.Equal(t, openapi_types.UUID(contact.UUID), *payload.ContactId)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadEngagementReply
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestEngagementReplyPayload_NoContact proves an unresolved counterparty
// omits contact_id from the wire body rather than marshaling it as null.
func TestEngagementReplyPayload_NoContact(t *testing.T) {
	matched := ids.NewV7()
	occurredAt := time.Date(2026, 7, 22, 9, 30, 0, 0, time.UTC)

	payload := engagementReplyPayload(matched, occurredAt, "gmail:msg-43", nil)

	require.Nil(t, payload.ContactId)
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "contact_id")
}
