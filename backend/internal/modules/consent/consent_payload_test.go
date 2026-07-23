// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package consent

// TDD Step 1 of the webhooks Task 5d migration (consent half): drives
// consentChangedPayload — the exact function Record calls to build its
// consent.changed emit (store.go) — then round-trips the result through
// JSON exactly as storekit.EmitEventForEntity marshals it into the outbox
// envelope's payload column. There is no non-integration harness in this
// repo that drives a Store method against a real Postgres (every such test
// lives under compose/integration, gated `//go:build integration`, needing
// db-up); testing the production payload-construction function directly —
// the one place a schema/code mismatch would show up — is the honest
// substitute, mirroring the deal family's
// TestDealStageChangedEmitsTypedPayload (webhooks Task 5a-i).
//
// consent.changed is the FIRST dynamic-entity type migrated (contract
// x-entity-type: dynamic): its subject is a person XOR a lead, a runtime
// choice consentSubject already resolves (subject_test.go proves that
// resolution). What this file additionally proves is the seam ON TOP of
// that resolution — that Record stages the event via
// storekit.EmitEventForEntity, passing sub.entityType as the wire
// entity_type, NOT the payload's own (unused, "dynamic") EntityType()
// method — using the same fakeTx boundary mock storekit's own
// emitevent_test.go uses, since consent (a module) may depend on storekit
// (platform) but not the other way around.
//
// Before this migration crmcontracts.PublicEventConsentChanged did not
// exist, and neither did consentChangedPayload, so this test failed to
// compile (RED) until public-events.yaml gained the schema, `make gen`
// regenerated the struct, and store.go grew the builder.

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

// TestConsentChangedPayload proves consentChangedPayload carries the exact
// purpose_id/purpose/new_state triple Record passes it, and that the result
// round-trips through JSON unchanged (the wire shape storekit.EmitEventForEntity
// marshals into the outbox envelope).
func TestConsentChangedPayload(t *testing.T) {
	purposeID := ids.New[ids.PurposeKind]()

	payload := consentChangedPayload(purposeID, "marketing_email", "granted")

	require.Equal(t, "consent.changed", payload.EventType())
	require.Equal(t, "dynamic", payload.EntityType(),
		"consent.changed is a dynamic-entity type — its static EntityType() is unused; the real subject comes from EmitEventForEntity's caller-supplied entityType")
	require.Equal(t, purposeID.UUID, ids.UUID(payload.PurposeId))
	require.Equal(t, "marketing_email", payload.Purpose)
	require.Equal(t, "granted", payload.NewState)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var decoded crmcontracts.PublicEventConsentChanged
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, payload, decoded)
}

// fakeTx is the true-DB-boundary fake (T11), mirroring
// storekit/emitevent_test.go's fakeTx: it implements only Exec meaningfully
// and captures the statement + args Emit hands it. Every other pgx.Tx
// method panics — EmitEventForEntity never calls them, so reaching one
// would be this test's own bug, not a legitimate path to stub out.
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

// TestConsentChangedEmitUsesRuntimeEntityType is the dynamic-entity twist
// this task's contract requires: consent.changed's subject is a person XOR
// a lead (data-model §7), a runtime choice consentSubject resolves — so
// Record must stage the event via storekit.EmitEventForEntity(entityType)
// rather than storekit.EmitEvent (which would derive the always-"dynamic"
// static EntityType() and misroute every envelope). Driving the exact same
// builder + seam Record uses against both subjects proves the wire
// entity_type tracks the runtime subject, not the payload's own type.
func TestConsentChangedEmitUsesRuntimeEntityType(t *testing.T) {
	purposeID := ids.New[ids.PurposeKind]()
	payload := consentChangedPayload(purposeID, "marketing_email", "granted")

	for _, tc := range []struct {
		name       string
		entityType string
	}{
		{name: "person subject", entityType: "person"},
		{name: "lead subject", entityType: "lead"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{}
			auditID := ids.NewV7()
			subjectID := ids.NewV7()

			err := storekit.EmitEventForEntity(emitTestContext(), tx, auditID, tc.entityType, subjectID, payload)
			require.NoError(t, err)

			require.Equal(t, tc.entityType, decodedOutboxEntityType(t, tx),
				"consent.changed must carry the runtime subject's entity type, not the payload's static (unused) EntityType()")
		})
	}
}
