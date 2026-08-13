// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// What the ledger seam does once it is live, over real migrated Postgres: the
// three rows a unit's write produces land in ONE transaction and leave together
// when it rolls back, the ledger row carries the attribution the core stamped,
// and the envelope on the bus is routable and joined to that ledger row.
//
// None of it is checkable without a database. Atomicity is a property of a
// transaction, the evidence merge happens inside an INSERT, and the envelope is
// validated on its way into the outbox — a fake at any of those three points
// would be asserting this test's own arithmetic.
//
// The unit's OWN row is a `workspace` update, for the reason
// TestRuntimeTxCommitsAndRollsBack gives: the ext_* tables arrive with a unit,
// and this seam has to be right before there is one. What is under test is the
// pairing, not the table.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/pkg/extension"
	"github.com/gradionhq/margince/backend/pkg/extension/crm"
)

// ledgerEntity is a table in the probe unit's namespace (`alpha` →
// `ext_alpha`). audit_log.entity_type is free text, so what makes this the
// unit's own is the port's check against the invoking unit — which is exactly
// what these rows are here to demonstrate landing.
const ledgerEntity = "ext_alpha_thing"

// auditedRow reads back the one ledger row written for an entity.
//
// It reads inside a WORKSPACE-BOUND transaction because audit_log carries
// forced row-level security: the app pool the lane runs on is bound per
// transaction, so the same query outside one matches nothing at all — and a
// count of zero read that way would look exactly like a write that never
// happened.
func auditedRow(t *testing.T, e *extRuntimeEnv, entityID ids.UUID) (auditID ids.UUID, action, actorID string, evidence []byte) {
	t.Helper()
	ctx := e.callCtx(e.WS)
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, action, actor_id, evidence FROM audit_log WHERE entity_type = $1 AND entity_id = $2`,
			ledgerEntity, entityID).Scan(&auditID, &action, &actorID, &evidence)
	}); err != nil {
		t.Fatalf("reading the ledger row for %s/%s: %v", ledgerEntity, entityID, err)
	}
	return auditID, action, actorID, evidence
}

// TestAUnitsOwnWriteLandsAsARowALedgerRowAndAnEvent: the write shape, for a
// table the core has never heard of. The unit describes its own change; the
// core supplies the actor, the workspace, the attribution and the trace.
func TestAUnitsOwnWriteLandsAsARowALedgerRowAndAnEvent(t *testing.T) {
	e := setupExtRuntime(t)
	rt, ctx := e.runtime("alpha")
	rowID := ids.NewV7()

	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE workspace SET slug = $1 WHERE id = $2`, "ledger-probe", e.WS); err != nil {
			return err
		}
		return tx.Record(ctx,
			extension.Change{
				Action: extension.AuditCreate,
				Entity: ledgerEntity,
				ID:     rowID.String(),
				After:  json.RawMessage(`{"body":"the row the unit wrote"}`),
				Detail: json.RawMessage(`{"cause":"a probe"}`),
			},
			extension.Event{Verb: "thing_added", Payload: json.RawMessage(`{"kind":"thing"}`)})
	}); err != nil {
		t.Fatal(err)
	}

	auditID, action, actorID, evidence := auditedRow(t, e, rowID)
	if action != string(extension.AuditCreate) {
		t.Errorf("the ledger row records %q, want %q", action, extension.AuditCreate)
	}
	// The actor is the INVOCATION's, never the unit: who acted and what carried
	// the action are different answers, and this is the first one.
	if actorID != "system:extruntime-test" {
		t.Errorf("actor_id = %q, want the invocation's own principal", actorID)
	}
	assertAttribution(t, evidence)

	envelope := publishedEnvelope(t, e, "ext_alpha.thing_added")
	if envelope.Version != events.ExtensionEventVersion {
		t.Errorf("the envelope claims version %d, want %d", envelope.Version, events.ExtensionEventVersion)
	}
	if envelope.Entity.Type != ledgerEntity || envelope.Entity.ID != rowID {
		t.Errorf("the envelope names %s/%s, want the audited row %s/%s",
			envelope.Entity.Type, envelope.Entity.ID, ledgerEntity, rowID)
	}
	// The join a consumer needs: the event and the governance record of the
	// same write. It is what makes an extension event auditable at all.
	if envelope.Trace.AuditLogID != auditID {
		t.Errorf("the envelope's trace names ledger row %s, want the one this write made (%s)",
			envelope.Trace.AuditLogID, auditID)
	}
	assertSameJSON(t, "the envelope's payload", envelope.Payload, `{"kind":"thing"}`)
}

// assertAttribution reads the core-stamped members and the unit's own detail
// out of one evidence document.
func assertAttribution(t *testing.T, evidence []byte) {
	t.Helper()
	var recorded struct {
		Extension struct {
			Unit    string          `json:"unit"`
			Version string          `json:"version"`
			Via     string          `json:"via"`
			Detail  json.RawMessage `json:"detail"`
		} `json:"extension"`
	}
	if err := json.Unmarshal(evidence, &recorded); err != nil {
		t.Fatalf("the ledger row's evidence does not decode: %v (%s)", err, evidence)
	}
	if recorded.Extension.Unit != "alpha" || recorded.Extension.Version != "1.0.0" {
		t.Errorf("evidence.extension names %s/%s, want the invoked unit alpha/1.0.0",
			recorded.Extension.Unit, recorded.Extension.Version)
	}
	if recorded.Extension.Via != "tool/probe" {
		t.Errorf("evidence.extension.via = %q, want the surface the call arrived on", recorded.Extension.Via)
	}
	// Compared as a VALUE, not as bytes. Both columns this rides through are
	// jsonb, which re-renders what it stores — whitespace and key order are
	// the database's from here on — so a unit's guarantee is that its document
	// survives, never that its formatting does.
	assertSameJSON(t, "evidence.extension.detail", recorded.Extension.Detail, `{"cause":"a probe"}`)
}

// publishedEnvelope reads back the one outbox row staged for a type, and
// asserts on its way past that the row was routed to the extension stream.
func publishedEnvelope(t *testing.T, e *extRuntimeEnv, eventType string) events.Envelope {
	t.Helper()
	var stream string
	var raw []byte
	ctx := e.callCtx(e.WS)
	// event_outbox is one of the infra tables outside RLS (0014), so this read
	// needs no binding of its own — it runs on the same pool as everything
	// else here for consistency, not for visibility.
	if err := database.WithWorkspaceTx(ctx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT stream, envelope FROM event_outbox WHERE envelope->>'type' = $1`, eventType).Scan(&stream, &raw)
	}); err != nil {
		t.Fatalf("reading the outbox row for %s: %v", eventType, err)
	}
	if stream != events.ExtensionStream() {
		t.Errorf("%s was staged on %q, want the extension stream", eventType, stream)
	}
	var envelope events.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("the staged envelope does not decode: %v", err)
	}
	return envelope
}

// assertSameJSON compares two JSON documents as values.
//
// Everything a unit publishes lands in a jsonb column, which normalizes what it
// stores: whitespace goes, key order becomes the database's, duplicate members
// collapse. So the honest assertion — and the honest promise to a unit — is
// about the document, not its bytes.
func assertSameJSON(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("%s does not decode: %v (%s)", what, err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("the expectation for %s does not decode: %v", what, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

// TestAFailedUnitWriteTakesItsLedgerRowAndItsEventWithIt: the ledger runs on
// the CALLER's transaction, so a handler that changes its mind afterwards
// leaves no history of a write that never happened. The rows are counted with
// the transaction already rolled back, which is the state a later reader meets.
func TestAFailedUnitWriteTakesItsLedgerRowAndItsEventWithIt(t *testing.T) {
	e := setupExtRuntime(t)
	rt, ctx := e.runtime("alpha")
	rowID := ids.NewV7()
	abandoned := errors.New("the handler changed its mind")

	if err := rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
		if err := tx.Record(ctx,
			extension.Change{
				Action: extension.AuditCreate, Entity: ledgerEntity, ID: rowID.String(),
				After: json.RawMessage(`{"body":"never committed"}`),
			},
			extension.Event{Verb: "thing_added"}); err != nil {
			return err
		}
		return abandoned
	}); !errors.Is(err, abandoned) {
		t.Fatalf("Tx returned %v, want the callback's own error", err)
	}

	var audits, staged int
	countCtx := e.callCtx(e.WS)
	if err := database.WithWorkspaceTx(countCtx, e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(countCtx,
			`SELECT (SELECT count(*) FROM audit_log WHERE entity_id = $1),
			        (SELECT count(*) FROM event_outbox WHERE envelope->'entity'->>'id' = $1::text)`,
			rowID).Scan(&audits, &staged)
	}); err != nil {
		t.Fatal(err)
	}
	if audits != 0 || staged != 0 {
		t.Errorf("a rolled-back write left %d ledger rows and %d staged events, want none", audits, staged)
	}
}

// TestADeliveryThroughTheRealHandlerRecordsAndRefuses drives the whole
// delivery path: an ENVELOPE goes into the handler the worker actually runs,
// and what comes out is a ledger row, an event, and a refused core write.
//
// It is deliberately not built out of deliveryRuntimeFor and a context of this
// test's own. Everything that would make a delivery wrong is established inside
// ComposedSubscription.Handler — the workspace it resolves, the system actor it
// binds, the correlation it carries through, the causation it links, and the
// unattended Runtime that shuts the core port — so a test that supplied those
// itself would be asserting its own arithmetic and would stay green through
// exactly the regression that matters.
func TestADeliveryThroughTheRealHandlerRecordsAndRefuses(t *testing.T) {
	e := setupExtRuntime(t)
	rowID, correlation, cause := ids.NewV7(), ids.NewV7(), ids.NewV7()

	var coreErr error
	sub := ComposedSubscription{
		Unit: "alpha", Version: "1.0.0",
		Sub: extension.Subscription{
			Name: "probe", Events: []string{"activity.archived"},
			Handle: func(ctx context.Context, rt extension.Runtime, d extension.Delivery) error {
				if rt.Caller() != (extension.Caller{}) {
					t.Errorf("the handler was given caller %+v, want nobody", rt.Caller())
				}
				if d.Type != "activity.archived" || d.Entity.Type != "activity" {
					t.Errorf("delivery = %+v, want the archived activity", d)
				}
				return rt.Tx(ctx, func(ctx context.Context, tx extension.Tx) error {
					_, coreErr = tx.Core().Activities().Create(ctx, crm.CreateActivityRequest{
						Kind: crm.CreateActivityRequestKindNote, Source: "extension:probe",
					})
					return tx.Record(ctx,
						extension.Change{
							Action: extension.AuditUpdate, Entity: ledgerEntity, ID: rowID.String(),
							After: json.RawMessage(`{"filed_activity_id":null}`),
						},
						extension.Event{Verb: "thing_changed"})
				})
			},
		},
	}

	// The envelope a real archive puts on the bus, correlation and all.
	if err := sub.Handler(e.Pool, slog.New(slog.DiscardHandler))(context.Background(), events.Envelope{
		EventID: cause, Type: "activity.archived", OccurredAt: time.Now().UTC(),
		Entity: events.EntityRef{Type: "activity", ID: ids.NewV7()},
		Trace:  events.Trace{CorrelationID: correlation, AuditLogID: ids.NewV7()},
	}); err != nil {
		t.Fatalf("the delivery failed: %v", err)
	}

	// The core port is shut for a caller auth.Require would not have checked.
	if !errors.Is(coreErr, extension.ErrForbidden) {
		t.Errorf("a delivery's core write answered %v, want ErrForbidden", coreErr)
	}

	// And the unit's own write landed, under the identity the HANDLER bound.
	_, action, actorID, evidence := auditedRow(t, e, rowID)
	if action != string(extension.AuditUpdate) {
		t.Errorf("the delivery's ledger row records %q, want an update", action)
	}
	if actorID != "system" {
		t.Errorf("actor_id = %q, want the system principal a delivery runs as", actorID)
	}
	var recorded struct {
		Extension struct{ Via string } `json:"extension"`
	}
	if err := json.Unmarshal(evidence, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.Extension.Via != "subscription/probe" {
		t.Errorf("evidence.extension.via = %q, want the subscription that ran", recorded.Extension.Via)
	}

	// The trace is the whole point of a reaction: the event this published
	// carries the correlation of the fact that caused it, and names that fact
	// as its cause. Read together they are one story.
	envelope := publishedEnvelope(t, e, "ext_alpha.thing_changed")
	if envelope.Trace.CorrelationID != correlation {
		t.Errorf("the reaction's correlation is %s, want the triggering event's %s",
			envelope.Trace.CorrelationID, correlation)
	}
	if envelope.Trace.CausationID == nil || *envelope.Trace.CausationID != cause {
		t.Errorf("the reaction's causation is %v, want the event that caused it (%s)",
			envelope.Trace.CausationID, cause)
	}
}

// TestAPanickingHandlerFailsTheDeliveryRatherThanTheWorker: nothing sits above
// a delivery but the subscriber's goroutine, so an unrecovered panic in one
// unit's listener takes down the relay, the job runner and every other unit's
// lane — and the entry that caused it is un-acked, so the restarted worker is
// handed the same event and dies again. One bad delivery would be an
// installation-wide crash loop.
//
// It runs in the database lane because the panic has to happen where the real
// one would: inside a handler that the real Handler actually reached, past the
// tenant read a fake pool cannot answer.
func TestAPanickingHandlerFailsTheDeliveryRatherThanTheWorker(t *testing.T) {
	e := setupExtRuntime(t)
	var ran bool
	sub := ComposedSubscription{
		Unit: "alpha", Version: "1.0.0",
		Sub: extension.Subscription{
			Name: "probe", Events: []string{"activity.archived"},
			Handle: func(context.Context, extension.Runtime, extension.Delivery) error {
				ran = true
				panic("a unit's own nil map")
			},
		},
	}

	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("the panic escaped the delivery and reached the lane: %v", r)
			}
		}()
		err = sub.Handler(e.Pool, slog.New(slog.DiscardHandler))(context.Background(), events.Envelope{
			EventID: ids.NewV7(), Type: "activity.archived", OccurredAt: time.Now().UTC(),
			Entity: events.EntityRef{Type: "activity", ID: ids.NewV7()},
			Trace:  events.Trace{CorrelationID: ids.NewV7(), AuditLogID: ids.NewV7()},
		})
	}()

	if !ran {
		t.Fatal("the handler never ran, so this proves nothing about a panic inside one")
	}
	if err == nil {
		t.Fatal("a panicking delivery was acked — the fact it panicked on would be dropped")
	}
	// The failure names the unit and the subscription: the bus entry cannot,
	// and an operator reading a failed delivery has nothing else to go on.
	for _, want := range []string{"alpha", "probe"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not name %q: %v", want, err)
		}
	}
}
