// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// The typed emit seam: EmitEvent derives event_type/entity_type from the
// payload struct itself (a wrong payload for an event is impossible to
// express), and EmitEventForEntity lets the 5 dynamic-entity event types
// (mirror.*, consent.changed, retention.applied) override entity_type with
// a runtime value.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TestPilotPayloadSatisfiesPayloadInterface proves Task 1's generated
// EventType()/EntityType() methods line up with events.Payload — it lives
// here rather than in the events package because events is Tier-0 shared
// (stdlib-only, TestSharedIsPure forbids a project import even from a test
// file) and crmcontracts is not stdlib; storekit (platform) is the nearest
// package the arch DAG lets import both (.go-arch-lint.yml: platform
// mayDependOn contracts).
func TestPilotPayloadSatisfiesPayloadInterface(t *testing.T) {
	var p events.Payload = crmcontracts.PublicEventDealStageChanged{}
	if got := p.EventType(); got != "deal.stage_changed" {
		t.Fatalf("EventType() = %q, want %q", got, "deal.stage_changed")
	}
	if got := p.EntityType(); got != "deal" {
		t.Fatalf("EntityType() = %q, want %q", got, "deal")
	}
}

// fakeTx is the true-DB-boundary fake (T11): it implements only Exec
// meaningfully and captures the statement + args Emit hands it. Every
// other pgx.Tx method panics — Emit never calls them, so reaching one
// would be this test's own bug, not a legitimate path to stub out.
type fakeTx struct {
	execSQL  string
	execArgs []any
	execErr  error
}

func (f *fakeTx) Exec(_ context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	f.execSQL = sql
	f.execArgs = arguments
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
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

// decodedOutboxRow unmarshals the envelope fakeTx captured from the
// INSERT INTO event_outbox(stream, envelope) VALUES ($1, $2) call.
func decodedOutboxRow(t *testing.T, tx *fakeTx) (stream string, env events.Envelope) {
	t.Helper()
	if !strings.Contains(tx.execSQL, "INSERT INTO event_outbox") {
		t.Fatalf("Exec SQL = %q, want it to contain %q", tx.execSQL, "INSERT INTO event_outbox")
	}
	if len(tx.execArgs) != 2 {
		t.Fatalf("Exec args = %v, want exactly 2 (stream, envelope)", tx.execArgs)
	}
	stream, ok := tx.execArgs[0].(string)
	if !ok {
		t.Fatalf("first Exec arg = %T, want string (the stream key)", tx.execArgs[0])
	}
	body, ok := tx.execArgs[1].([]byte)
	if !ok {
		t.Fatalf("second Exec arg = %T, want []byte (the marshaled envelope)", tx.execArgs[1])
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshaling the staged envelope: %v", err)
	}
	return stream, env
}

// TestEmitEvent_derivesTypeAndEntityFromPayload proves EmitEvent stages an
// outbox row whose event_type/entity_type/payload all come from the
// payload struct itself — a wrong payload for an event, or a payload
// staged under the wrong entity type, is impossible to express because
// EmitEvent never takes those strings as separate parameters a caller
// could mismatch.
func TestEmitEvent_derivesTypeAndEntityFromPayload(t *testing.T) {
	ctx := emitTestContext()
	tx := &fakeTx{}
	auditID := ids.NewV7()
	dealID := ids.NewV7()
	payload := crmcontracts.PublicEventDealStageChanged{}

	if err := EmitEvent(ctx, tx, auditID, dealID, payload); err != nil {
		t.Fatalf("EmitEvent: %v", err)
	}

	stream, env := decodedOutboxRow(t, tx)
	if want := events.StreamPrefix + "deal"; stream != want {
		t.Fatalf("stream = %q, want %q", stream, want)
	}
	if env.Type != "deal.stage_changed" {
		t.Fatalf("envelope.Type = %q, want %q", env.Type, "deal.stage_changed")
	}
	if env.Entity.Type != "deal" {
		t.Fatalf("envelope.Entity.Type = %q, want %q", env.Entity.Type, "deal")
	}
	if env.Entity.ID != dealID {
		t.Fatalf("envelope.Entity.ID = %v, want %v", env.Entity.ID, dealID)
	}
	if env.Trace.AuditLogID != auditID {
		t.Fatalf("envelope.Trace.AuditLogID = %v, want %v", env.Trace.AuditLogID, auditID)
	}

	var decodedPayload crmcontracts.PublicEventDealStageChanged
	if err := json.Unmarshal(env.Payload, &decodedPayload); err != nil {
		t.Fatalf("unmarshaling the staged payload: %v", err)
	}
}

// TestEmitEventForEntity_overridesEntityType proves the caller-supplied
// entityType wins over the payload's own EntityType() — the seam the 5
// dynamic-entity event types need, since their subject is a runtime value
// the payload's static type cannot name.
func TestEmitEventForEntity_overridesEntityType(t *testing.T) {
	ctx := emitTestContext()
	tx := &fakeTx{}
	auditID := ids.NewV7()
	runtimeEntityID := ids.NewV7()
	payload := crmcontracts.PublicEventDealStageChanged{}

	if err := EmitEventForEntity(ctx, tx, auditID, "consent_purpose", runtimeEntityID, payload); err != nil {
		t.Fatalf("EmitEventForEntity: %v", err)
	}

	_, env := decodedOutboxRow(t, tx)
	if env.Type != "deal.stage_changed" {
		t.Fatalf("envelope.Type = %q, want %q (event type still comes from the payload)", env.Type, "deal.stage_changed")
	}
	if env.Entity.Type != "consent_purpose" {
		t.Fatalf("envelope.Entity.Type = %q, want the caller-supplied override %q, not payload.EntityType() (\"deal\")", env.Entity.Type, "consent_purpose")
	}
	if env.Entity.ID != runtimeEntityID {
		t.Fatalf("envelope.Entity.ID = %v, want %v", env.Entity.ID, runtimeEntityID)
	}
}
