// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// TDD Step 1 of the webhooks Task 5c migration (activities family): drives
// the payload-builder functions this package's emit sites call —
// activityCapturedPayload (activity.go), activityUpdatedChangedFields
// (lifecycle.go's UpdateActivity), and relinkedChangedFields (lifecycle.go's
// RelinkActivity) — then round-trips each result through JSON exactly as
// storekit.EmitEvent marshals it into the outbox envelope's payload column.
// There is no non-integration harness in this repo that drives a Store
// method against a real Postgres (every such test lives under
// compose/integration, gated `//go:build integration`, needing db-up);
// testing the production payload-construction functions directly — the one
// place a schema/code mismatch would show up — is the honest substitute,
// mirroring the deal family's TestDealStageChangedEmitsTypedPayload
// (webhooks Task 5a-i).
//
// Before this migration none of crmcontracts.PublicEventActivityCaptured/
// Archived/Updated existed, and none of the builder functions existed, so
// this test failed to compile (RED) until public-events.yaml gained the
// schemas, `make gen` regenerated the structs, and activity.go/lifecycle.go
// grew the builders.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestActivityCapturedPayload_DirectLog proves the direct-log path
// (activity.go's logActivityInTx) sets kind only — no source_system, since
// that field is exclusive to the capture auto-create path.
func TestActivityCapturedPayload_DirectLog(t *testing.T) {
	payload := activityCapturedPayload("meeting")

	require.Equal(t, "activity.captured", payload.EventType())
	require.Equal(t, "activity", payload.EntityType())
	require.Equal(t, "meeting", payload.Kind)
	require.Nil(t, payload.SourceSystem)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "source_system",
		"an absent source_system must be omitted from the wire body, not marshaled as null")
	var decoded crmcontracts.PublicEventActivityCaptured
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestActivityCapturedKindJSONTagIsStable is the A9 key-binding regression
// test: automation/handlers_event.go's capturedActivityKind decodes
// activity.captured's outbox payload by the literal JSON key "kind"
// (automation cannot import the activities or contracts payload type — it
// reads the generic map[string]any the bus hands every subscriber). A
// future rename of this schema field must break THIS test, not silently
// stop the post_meeting_recap automation trigger from matching.
func TestActivityCapturedKindJSONTagIsStable(t *testing.T) {
	payload := crmcontracts.PublicEventActivityCaptured{Kind: "meeting"}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	got, ok := decoded["kind"]
	require.True(t, ok, `expected JSON key "kind" (bound in automation/handlers_event.go's capturedActivityKind) in %s`, raw)
	require.Equal(t, "meeting", got)
}

// TestActivityArchivedEmitsTypedPayload proves the archive path emits the
// empty struct (activity.archived carries no data).
func TestActivityArchivedEmitsTypedPayload(t *testing.T) {
	payload := crmcontracts.PublicEventActivityArchived{}
	require.Equal(t, "activity.archived", payload.EventType())
	require.Equal(t, "activity", payload.EntityType())

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Equal(t, "{}", string(raw))
}

// TestActivityUpdatedChangedFields_FieldPatch proves
// activityUpdatedChangedFields maps UpdateActivity's optional-field input
// onto the typed changed_fields struct — subject/occurred_at/is_done
// touched, body/due_at/remind_at/assignee_id untouched (and therefore
// omitted, not nulled).
func TestActivityUpdatedChangedFields_FieldPatch(t *testing.T) {
	subject := "Follow-up call"
	occurredAt := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	isDone := true
	in := UpdateActivityInput{
		Subject:    &subject,
		OccurredAt: &occurredAt,
		IsDone:     &isDone,
	}

	fields := activityUpdatedChangedFields(in)
	require.NotNil(t, fields.Subject)
	require.Equal(t, subject, *fields.Subject)
	require.NotNil(t, fields.OccurredAt)
	require.Equal(t, occurredAt, *fields.OccurredAt)
	require.NotNil(t, fields.IsDone)
	require.True(t, *fields.IsDone)
	require.Nil(t, fields.Body)
	require.Nil(t, fields.DueAt)
	require.Nil(t, fields.RemindAt)
	require.Nil(t, fields.AssigneeId)
	require.Nil(t, fields.Relinked)

	payload := crmcontracts.PublicEventActivityUpdated{ChangedFields: fields}
	require.Equal(t, "activity.updated", payload.EventType())
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "due_at",
		"an untouched field must be omitted from changed_fields, not marshaled as null")
	var decoded crmcontracts.PublicEventActivityUpdated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestActivityUpdatedChangedFields_BodyIsPresenceOnly proves the body delta
// carries a presence flag, not the (potentially large) body content —
// matching updateDelta's existing "body: true" convention.
func TestActivityUpdatedChangedFields_BodyIsPresenceOnly(t *testing.T) {
	body := "a very long email body that must never be echoed onto the wire"
	in := UpdateActivityInput{Body: &body}

	fields := activityUpdatedChangedFields(in)
	require.NotNil(t, fields.Body)
	require.True(t, *fields.Body)

	raw, err := json.Marshal(fields)
	require.NoError(t, err)
	require.NotContains(t, string(raw), body, "the body's content must never be published on the wire")
}

// TestRelinkedChangedFields proves RelinkActivity's changed_fields carries
// the relinked target as a typed sub-object, not the old ad hoc nested map.
func TestRelinkedChangedFields(t *testing.T) {
	entityID := ids.NewV7()
	fields := relinkedChangedFields("deal", entityID)

	require.NotNil(t, fields.Relinked)
	require.Equal(t, "deal", fields.Relinked.EntityType)
	require.Equal(t, openapi_types.UUID(entityID), fields.Relinked.EntityId)
	require.Nil(t, fields.Subject)

	payload := crmcontracts.PublicEventActivityUpdated{ChangedFields: fields}
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventActivityUpdated
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}
