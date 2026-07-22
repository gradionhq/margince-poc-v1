// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package privacy

// TDD Step 1 of the webhooks Task 5d migration (privacy half): drives
// retentionAppliedPayload — the exact function all three retention.applied
// emit sites call (retention.go's eraseEmbedCall and apply, erasure.go's
// ErasePerson) — then round-trips the result through JSON exactly as
// storekit.EmitEventForEntity marshals it into the outbox envelope's
// payload column. There is no non-integration harness in this repo that
// drives a Store/Service method against a real Postgres (every such test
// lives under compose/integration, gated `//go:build integration`, needing
// db-up); testing the production payload-construction function directly —
// the one place a schema/code mismatch would show up — is the honest
// substitute, mirroring the deal family's
// TestDealStageChangedEmitsTypedPayload (webhooks Task 5a-i).
//
// retention.applied is dynamic-entity (contract x-entity-type: dynamic):
// its subject is ai_call (the embedding-retention sweep), pol.ObjectType
// (a workspace's configured retention policy — activity/deal/lead/person/
// ai_call_payload), or person (Art. 17 erasure) — three DIFFERENT runtime
// values across the three sites, none of which is the payload's own
// (unused, "dynamic") EntityType(). This file proves each site's
// entity-type expression survives into the wire envelope via
// storekit.EmitEventForEntity, using the same fakeTx boundary mock
// storekit's own emitevent_test.go uses, since privacy (a module) may
// depend on storekit (platform) but not the other way around.
//
// Before this migration crmcontracts.WebhookPayloadRetentionApplied did not
// exist, and neither did retentionAppliedPayload, so this test failed to
// compile (RED) until public-events.yaml gained the schema, `make gen`
// regenerated the struct, and retention.go/erasure.go grew the builder.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestRetentionAppliedPayload_ActionOnly proves the embed-call sweep's
// subset (retention.go's eraseEmbedCall): action only, no policy or reason.
func TestRetentionAppliedPayload_ActionOnly(t *testing.T) {
	payload := retentionAppliedPayload(actionErase, nil, nil)

	require.Equal(t, "retention.applied", payload.EventType())
	require.Equal(t, "dynamic", payload.EntityType(),
		"retention.applied is a dynamic-entity type — its static EntityType() is unused; the real subject comes from EmitEventForEntity's caller-supplied entityType")
	require.Equal(t, actionErase, payload.Action)
	require.Nil(t, payload.Policy)
	require.Nil(t, payload.Reason)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "policy")
	require.NotContains(t, string(raw), "reason")
	var decoded crmcontracts.WebhookPayloadRetentionApplied
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// TestRetentionAppliedPayload_WithPolicy proves the policy-driven sweep's
// subset (retention.go's apply): action + policy, no reason.
func TestRetentionAppliedPayload_WithPolicy(t *testing.T) {
	policyID := ids.NewV7()

	payload := retentionAppliedPayload("archive", &policyID, nil)

	require.Equal(t, "archive", payload.Action)
	require.NotNil(t, payload.Policy)
	require.Equal(t, policyID, ids.UUID(*payload.Policy))
	require.Nil(t, payload.Reason)
}

// TestRetentionAppliedPayload_WithReason proves the Art. 17 erasure
// subset (erasure.go's ErasePerson): action + reason, no policy.
func TestRetentionAppliedPayload_WithReason(t *testing.T) {
	reason := "dsr_request"

	payload := retentionAppliedPayload(actionErase, nil, &reason)

	require.Equal(t, actionErase, payload.Action)
	require.Nil(t, payload.Policy)
	require.NotNil(t, payload.Reason)
	require.Equal(t, reason, *payload.Reason)
}

// fakeTx is the true-DB-boundary fake (T11), mirroring
// storekit/emitevent_test.go's fakeTx: it implements only Exec
// meaningfully and captures the statement + args Emit hands it. Every
// other pgx.Tx method panics — EmitEventForEntity never calls them, so
// reaching one would be this test's own bug, not a legitimate path to
// stub out.
type fakeTx struct {
	execSQL  string
	execArgs []any
}

func (f *fakeTx) Exec(_ context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	f.execSQL = sql
	f.execArgs = arguments
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (f *fakeTx) Begin(context.Context) (pgx.Tx, error) { panic("fakeTx: Begin not implemented") }
func (f *fakeTx) Commit(context.Context) error          { panic("fakeTx: Commit not implemented") }
func (f *fakeTx) Rollback(context.Context) error        { panic("fakeTx: Rollback not implemented") }
func (f *fakeTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	panic("fakeTx: CopyFrom not implemented")
}
func (f *fakeTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	panic("fakeTx: SendBatch not implemented")
}
func (f *fakeTx) LargeObjects() pgx.LargeObjects { panic("fakeTx: LargeObjects not implemented") }
func (f *fakeTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	panic("fakeTx: Prepare not implemented")
}
func (f *fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("fakeTx: Query not implemented")
}
func (f *fakeTx) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("fakeTx: QueryRow not implemented")
}
func (f *fakeTx) Conn() *pgx.Conn { panic("fakeTx: Conn not implemented") }

// emitTestContext binds the actor/workspace/correlation triple Emit
// requires, exactly as the HTTP middleware would for a real request.
func emitTestContext() context.Context {
	ctx := context.Background()
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalHuman, ID: "human:" + ids.NewV7().String()})
	ctx = principal.WithWorkspaceID(ctx, ids.NewV7())
	ctx = principal.WithCorrelationID(ctx, ids.NewV7())
	return ctx
}

// decodedOutboxEntityType unmarshals just the entity ref off the envelope
// fakeTx captured from the INSERT INTO event_outbox(stream, envelope) call.
func decodedOutboxEntityType(t *testing.T, tx *fakeTx) string {
	t.Helper()
	require.Contains(t, tx.execSQL, "INSERT INTO event_outbox")
	require.Len(t, tx.execArgs, 2)
	body, ok := tx.execArgs[1].([]byte)
	require.True(t, ok, "second Exec arg = %T, want []byte (the marshaled envelope)", tx.execArgs[1])
	var env events.Envelope
	require.NoError(t, json.Unmarshal(body, &env))
	return env.Entity.Type
}

// TestRetentionAppliedEmitUsesRuntimeEntityType is the dynamic-entity twist
// this task's contract requires: retention.applied's subject varies by
// site — ai_call (the embed-call sweep), a policy's configured object type
// (the policy-driven sweep), or person (Art. 17 erasure) — none of which
// is the payload's own (unused, "dynamic") EntityType(). Driving the exact
// same seam each site uses against all three runtime values proves the
// wire entity_type tracks the caller-supplied subject, not the payload's
// static type.
func TestRetentionAppliedEmitUsesRuntimeEntityType(t *testing.T) {
	payload := retentionAppliedPayload(actionErase, nil, nil)

	for _, entityType := range []string{"ai_call", "activity", "deal", "person"} {
		t.Run(entityType, func(t *testing.T) {
			tx := &fakeTx{}
			auditID := ids.NewV7()
			subjectID := ids.NewV7()

			err := storekit.EmitEventForEntity(emitTestContext(), tx, auditID, entityType, subjectID, payload)
			require.NoError(t, err)

			require.Equal(t, entityType, decodedOutboxEntityType(t, tx),
				"retention.applied must carry the site's runtime entity type, not the payload's static (unused) EntityType()")
		})
	}
}
