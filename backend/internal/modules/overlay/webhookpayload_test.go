// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// TDD Step 1 of the webhooks Task 5h migration (overlay family): drives
// the payload-builder functions the package's five emit sites call —
// mirrorConflictPayload (reconcile.go's emitMirrorConflict),
// mirrorBudgetDegradedPayload (freshness.go's emitBudgetDegraded),
// mirrorDeletedPayload (mirrordeletion.go's PurgeDeletions),
// incumbentConnectedPayload (connection.go's insertConnection), and
// incumbentDisconnectedPayload (teardown.go's Disconnect) — then
// round-trips each result through JSON exactly as storekit.Emit/
// EmitEventForEntity marshal it into the outbox envelope's payload
// column, mirroring the identity family's TestUserInvitedPayload
// (webhooks Task 5g) and the consent/privacy family's dynamic-entity
// flow test (webhooks Task 5d).
//
// The three mirror.* events are dynamic-entity (contract x-entity-type:
// dynamic): each site's subject class is a RUNTIME string (rec.
// ObjectClass, string(ref.Type), del.ObjectClass), never the payload's
// own (unused, "dynamic") EntityType() — so this file also proves, via
// the same fakeTx boundary mock storekit's own emitevent_test.go uses,
// that storekit.EmitEventForEntity carries the CALLER-supplied entity
// type through to the wire envelope. incumbent.connected/disconnected
// are static-entity (always incumbent_connection), so no such flow test
// is needed for them — EmitEvent derives the entity type from the
// payload's own EntityType() (as proven by the builder tests below).
//
// Before this migration none of crmcontracts.WebhookPayloadMirrorConflict/
// MirrorBudgetDegraded/MirrorDeleted/IncumbentConnected/
// IncumbentDisconnected existed, and neither did any of the builder
// functions (every site inlined a map[string]any), so this test failed to
// compile (RED) until public-events.yaml gained the schemas, `make gen`
// regenerated the structs, and reconcile.go/freshness.go/
// mirrordeletion.go/connection.go/teardown.go grew the builders.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestMirrorConflictPayload(t *testing.T) {
	prior := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	incumbent := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	payload := mirrorConflictPayload("deal", "ext-42", prior, incumbent)

	require.Equal(t, "mirror.conflict", payload.EventType())
	require.Equal(t, "dynamic", payload.EntityType(),
		"mirror.conflict is a dynamic-entity type — its static EntityType() is unused; the real subject comes from EmitEventForEntity's caller-supplied entityType")
	require.Equal(t, "deal", payload.ObjectClass)
	require.Equal(t, "ext-42", payload.ExternalId)
	require.True(t, prior.Equal(payload.PriorUpdatedAt))
	require.True(t, incumbent.Equal(payload.IncumbentUpdatedAt))

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadMirrorConflict
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.True(t, decoded.PriorUpdatedAt.Equal(payload.PriorUpdatedAt))
	require.True(t, decoded.IncumbentUpdatedAt.Equal(payload.IncumbentUpdatedAt))
}

func TestMirrorBudgetDegradedPayload(t *testing.T) {
	payload := mirrorBudgetDegradedPayload(BandShed)

	require.Equal(t, "mirror.budget_degraded", payload.EventType())
	require.Equal(t, "dynamic", payload.EntityType(),
		"mirror.budget_degraded is a dynamic-entity type — its static EntityType() is unused; the real subject comes from EmitEventForEntity's caller-supplied entityType")
	require.Equal(t, BandShed, payload.Band)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadMirrorBudgetDegraded
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestMirrorDeletedPayload(t *testing.T) {
	deletedAt := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	payload := mirrorDeletedPayload("person", "ext-99", deletedAt)

	require.Equal(t, "mirror.deleted", payload.EventType())
	require.Equal(t, "dynamic", payload.EntityType(),
		"mirror.deleted is a dynamic-entity type — its static EntityType() is unused; the real subject comes from EmitEventForEntity's caller-supplied entityType")
	require.Equal(t, "person", payload.ObjectClass)
	require.Equal(t, "ext-99", payload.ExternalId)
	require.True(t, deletedAt.Equal(payload.DeletedAt))

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadMirrorDeleted
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.True(t, decoded.DeletedAt.Equal(payload.DeletedAt))
}

func TestIncumbentConnectedPayload(t *testing.T) {
	payload := incumbentConnectedPayload("hubspot", "eu", leastPrivilegeHubSpotScopes, statusActive)

	require.Equal(t, "incumbent.connected", payload.EventType())
	require.Equal(t, "incumbent_connection", payload.EntityType())
	require.Equal(t, "hubspot", payload.Incumbent)
	require.Equal(t, "eu", payload.Region)
	require.Equal(t, leastPrivilegeHubSpotScopes, payload.Scopes)
	require.Equal(t, statusActive, payload.Status)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadIncumbentConnected
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

func TestIncumbentDisconnectedPayload(t *testing.T) {
	payload := incumbentDisconnectedPayload("hubspot", "eu", statusRevoked)

	require.Equal(t, "incumbent.disconnected", payload.EventType())
	require.Equal(t, "incumbent_connection", payload.EntityType())
	require.Equal(t, "hubspot", payload.Incumbent)
	require.Equal(t, "eu", payload.Region)
	require.Equal(t, statusRevoked, payload.Status)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.WebhookPayloadIncumbentDisconnected
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// fakeTx is the true-DB-boundary fake (T11), mirroring
// storekit/emitevent_test.go's fakeTx (and consent/consent_payload_test.
// go's own copy): it implements only Exec meaningfully and captures the
// LAST statement + args Emit hands it. Every other pgx.Tx method panics —
// EmitEventForEntity never calls them, so reaching one would be this
// test's own bug, not a legitimate path to stub out.
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

// TestMirrorEventsEmitUseRuntimeEntityType is the dynamic-entity twist
// this task's contract requires: every mirror.* event's subject class is
// a runtime string (rec.ObjectClass / ref.Type / del.ObjectClass) — so
// each site must stage its event via storekit.EmitEventForEntity(entityType)
// rather than storekit.EmitEvent (which would derive the always-"dynamic"
// static EntityType() and misroute every envelope). Driving each payload
// through the same seam the real sites use proves the wire entity_type
// tracks the runtime subject, not the payload's own type.
func TestMirrorEventsEmitUseRuntimeEntityType(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name       string
		entityType string
		payload    events.Payload
	}{
		{name: "mirror.conflict/deal", entityType: "deal", payload: mirrorConflictPayload("deal", "ext-1", now, now)},
		{name: "mirror.conflict/person", entityType: "person", payload: mirrorConflictPayload("person", "ext-2", now, now)},
		{name: "mirror.budget_degraded/organization", entityType: "organization", payload: mirrorBudgetDegradedPayload(BandShed)},
		{name: "mirror.deleted/lead", entityType: "lead", payload: mirrorDeletedPayload("lead", "ext-3", now)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{}
			auditID := ids.NewV7()
			subjectID := ids.NewV7()

			err := storekit.EmitEventForEntity(emitTestContext(), tx, auditID, tc.entityType, subjectID, tc.payload)
			require.NoError(t, err)

			require.Equal(t, tc.entityType, decodedOutboxEntityType(t, tx),
				"%s must carry the runtime subject's entity type, not the payload's static (unused) EntityType()", tc.name)
		})
	}
}
