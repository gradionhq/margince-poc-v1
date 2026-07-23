// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The overlay family: drives the payload-builder functions the package's
// five emit sites call —
// mirrorConflictPayload (reconcile.go's emitMirrorConflict),
// mirrorBudgetDegradedPayload (freshness.go's emitBudgetDegraded),
// mirrorDeletedPayload (mirrordeletion.go's PurgeDeletions),
// incumbentConnectedPayload (connection.go's insertConnection), and
// incumbentDisconnectedPayload (teardown.go's Disconnect) — then
// round-trips each result through JSON exactly as storekit.Emit/
// EmitEventForEntity marshal it into the outbox envelope's payload column.
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

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestMirrorConflictPayload(t *testing.T) {
	prior := time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	incumbent := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	payload := mirrorConflictPayload("deal", "ext-42", prior, incumbent)

	if !reflect.DeepEqual(payload.EventType(), "mirror.conflict") {
		t.Errorf("got %v, want %v", payload.EventType(), "mirror.conflict")
	}
	if !reflect.DeepEqual(payload.EntityType(), "dynamic") {
		t.Errorf("mirror.conflict is a dynamic-entity type — its static EntityType() is unused; the real subject comes from EmitEventForEntity's caller-supplied entityType: got %v, want %v", payload.EntityType(), "dynamic")
	}
	if !reflect.DeepEqual(payload.ObjectClass, "deal") {
		t.Errorf("got %v, want %v", payload.ObjectClass, "deal")
	}
	if !reflect.DeepEqual(payload.ExternalId, "ext-42") {
		t.Errorf("got %v, want %v", payload.ExternalId, "ext-42")
	}
	if !prior.Equal(payload.PriorUpdatedAt) {
		t.Error("expected the condition to be true")
	}
	if !incumbent.Equal(payload.IncumbentUpdatedAt) {
		t.Error("expected the condition to be true")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventMirrorConflict
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !decoded.PriorUpdatedAt.Equal(payload.PriorUpdatedAt) {
		t.Error("expected the condition to be true")
	}
	if !decoded.IncumbentUpdatedAt.Equal(payload.IncumbentUpdatedAt) {
		t.Error("expected the condition to be true")
	}
}

func TestMirrorBudgetDegradedPayload(t *testing.T) {
	payload := mirrorBudgetDegradedPayload(overlaybudget.BandShed)

	if !reflect.DeepEqual(payload.EventType(), "mirror.budget_degraded") {
		t.Errorf("got %v, want %v", payload.EventType(), "mirror.budget_degraded")
	}
	if !reflect.DeepEqual(payload.EntityType(), "dynamic") {
		t.Errorf("mirror.budget_degraded is a dynamic-entity type — its static EntityType() is unused; the real subject comes from EmitEventForEntity's caller-supplied entityType: got %v, want %v", payload.EntityType(), "dynamic")
	}
	if !reflect.DeepEqual(payload.Band, overlaybudget.BandShed) {
		t.Errorf("got %v, want %v", payload.Band, overlaybudget.BandShed)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventMirrorBudgetDegraded
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestMirrorDeletedPayload(t *testing.T) {
	deletedAt := time.Date(2026, 7, 22, 11, 0, 0, 0, time.UTC)
	payload := mirrorDeletedPayload("person", "ext-99", deletedAt)

	if !reflect.DeepEqual(payload.EventType(), "mirror.deleted") {
		t.Errorf("got %v, want %v", payload.EventType(), "mirror.deleted")
	}
	if !reflect.DeepEqual(payload.EntityType(), "dynamic") {
		t.Errorf("mirror.deleted is a dynamic-entity type — its static EntityType() is unused; the real subject comes from EmitEventForEntity's caller-supplied entityType: got %v, want %v", payload.EntityType(), "dynamic")
	}
	if !reflect.DeepEqual(payload.ObjectClass, "person") {
		t.Errorf("got %v, want %v", payload.ObjectClass, "person")
	}
	if !reflect.DeepEqual(payload.ExternalId, "ext-99") {
		t.Errorf("got %v, want %v", payload.ExternalId, "ext-99")
	}
	if !deletedAt.Equal(payload.DeletedAt) {
		t.Error("expected the condition to be true")
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventMirrorDeleted
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !decoded.DeletedAt.Equal(payload.DeletedAt) {
		t.Error("expected the condition to be true")
	}
}

func TestIncumbentConnectedPayload(t *testing.T) {
	payload := incumbentConnectedPayload("hubspot", "eu", leastPrivilegeHubSpotScopes, statusActive)

	if !reflect.DeepEqual(payload.EventType(), "incumbent.connected") {
		t.Errorf("got %v, want %v", payload.EventType(), "incumbent.connected")
	}
	if !reflect.DeepEqual(payload.EntityType(), "incumbent_connection") {
		t.Errorf("got %v, want %v", payload.EntityType(), "incumbent_connection")
	}
	if !reflect.DeepEqual(payload.Incumbent, "hubspot") {
		t.Errorf("got %v, want %v", payload.Incumbent, "hubspot")
	}
	if !reflect.DeepEqual(payload.Region, "eu") {
		t.Errorf("got %v, want %v", payload.Region, "eu")
	}
	if !reflect.DeepEqual(payload.Scopes, leastPrivilegeHubSpotScopes) {
		t.Errorf("got %v, want %v", payload.Scopes, leastPrivilegeHubSpotScopes)
	}
	if !reflect.DeepEqual(payload.Status, statusActive) {
		t.Errorf("got %v, want %v", payload.Status, statusActive)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventIncumbentConnected
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
}

func TestIncumbentDisconnectedPayload(t *testing.T) {
	payload := incumbentDisconnectedPayload("hubspot", "eu", statusRevoked)

	if !reflect.DeepEqual(payload.EventType(), "incumbent.disconnected") {
		t.Errorf("got %v, want %v", payload.EventType(), "incumbent.disconnected")
	}
	if !reflect.DeepEqual(payload.EntityType(), "incumbent_connection") {
		t.Errorf("got %v, want %v", payload.EntityType(), "incumbent_connection")
	}
	if !reflect.DeepEqual(payload.Incumbent, "hubspot") {
		t.Errorf("got %v, want %v", payload.Incumbent, "hubspot")
	}
	if !reflect.DeepEqual(payload.Region, "eu") {
		t.Errorf("got %v, want %v", payload.Region, "eu")
	}
	if !reflect.DeepEqual(payload.Status, statusRevoked) {
		t.Errorf("got %v, want %v", payload.Status, statusRevoked)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded crmcontracts.PublicEventIncumbentDisconnected
	if json.Unmarshal(raw, &decoded) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(raw, &decoded))
	}
	if !reflect.DeepEqual(decoded, payload) {
		t.Errorf("got %v, want %v", decoded, payload)
	}
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
	if !strings.Contains(tx.execSQL, "INSERT INTO event_outbox") {
		t.Fatalf("%q should contain %q", tx.execSQL, "INSERT INTO event_outbox")
	}
	if len(tx.execArgs) != 2 {
		// Fatal, not Error: the index below would panic on a short slice,
		// masking the real "wrong arg count" failure.
		t.Fatalf("len = %d, want %d", len(tx.execArgs), 2)
	}
	body, ok := tx.execArgs[1].([]byte)
	if !ok {
		// Fatal, not Error: a nil body would make json.Unmarshal below report
		// a confusing "unexpected end of JSON input" instead of the real
		// "wrong argument type" failure.
		t.Fatalf("second Exec arg = %T, want []byte (the marshaled envelope)", tx.execArgs[1])
	}
	var env events.Envelope
	if json.Unmarshal(body, &env) != nil {
		t.Fatalf("unexpected error: %v", json.Unmarshal(body, &env))
	}
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
		{name: "mirror.budget_degraded/organization", entityType: "organization", payload: mirrorBudgetDegradedPayload(overlaybudget.BandShed)},
		{name: "mirror.deleted/lead", entityType: "lead", payload: mirrorDeletedPayload("lead", "ext-3", now)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{}
			auditID := ids.NewV7()
			subjectID := ids.NewV7()

			err := storekit.EmitEventForEntity(emitTestContext(), tx, auditID, tc.entityType, subjectID, tc.payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !reflect.DeepEqual(decodedOutboxEntityType(t, tx), tc.entityType) {
				t.Errorf("%s must carry the runtime subject's entity type, not the payload's static (unused) EntityType(): got %v, want %v", tc.name, decodedOutboxEntityType(t, tx), tc.entityType)
			}
		})
	}
}
