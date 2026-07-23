// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// fullInternalEnvelope builds an envelope with every internal-only field
// populated (WorkspaceID, Actor.PassportID/OnBehalfOf,
// Trace.CausationID/AuditLogID) so a test can prove toWireEnvelope drops
// every one of them rather than merely omitting a field nobody set.
func fullInternalEnvelope(t *testing.T) kevents.Envelope {
	t.Helper()
	passportID := ids.NewV7()
	onBehalfOf := ids.NewV7()
	causationID := ids.NewV7()
	return kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        "deal.created",
		Version:     1,
		WorkspaceID: ids.NewV7(),
		OccurredAt:  time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
		Actor: kevents.Actor{
			Type:       "human",
			ID:         "human:abc",
			PassportID: &passportID,
			OnBehalfOf: &onBehalfOf,
		},
		Entity:  kevents.EntityRef{Type: "deal", ID: ids.NewV7()},
		Payload: json.RawMessage(`{"stage_id":"s-1","amount":42}`),
		Trace: kevents.Trace{
			CorrelationID: ids.NewV7(),
			CausationID:   &causationID,
			AuditLogID:    ids.NewV7(),
		},
	}
}

// TestToWireEnvelopeIsThePublicDeliveryShape proves the mapping this task
// adds: the body a fresh event is delivered/enqueued with is the PUBLIC
// envelope, not the internal kevents.Envelope. It fails to compile until
// toWireEnvelope exists, and fails on the assertions if HandleEvent (or this
// function) ever again marshals the internal shape.
func TestToWireEnvelopeIsThePublicDeliveryShape(t *testing.T) {
	env := fullInternalEnvelope(t)

	wire, err := toWireEnvelope(env)
	if err != nil {
		t.Fatalf("toWireEnvelope: %v", err)
	}

	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshaling the wire envelope: %v", err)
	}
	s := string(body)
	for _, internal := range []string{"audit_log_id", "causation_id", "passport_id", "on_behalf_of", "workspace_id"} {
		if strings.Contains(s, internal) {
			t.Errorf("delivered body leaks internal field %q: %s", internal, s)
		}
	}

	if ids.UUID(wire.EventId) != env.EventID {
		t.Errorf("event_id = %v, want %v", wire.EventId, env.EventID)
	}
	if wire.Type != env.Type {
		t.Errorf("type = %q, want %q", wire.Type, env.Type)
	}
	if wire.Version != env.Version {
		t.Errorf("version = %d, want %d", wire.Version, env.Version)
	}
	if !wire.OccurredAt.Equal(env.OccurredAt) {
		t.Errorf("occurred_at = %v, want %v", wire.OccurredAt, env.OccurredAt)
	}
	if wire.Actor.Type != env.Actor.Type {
		t.Errorf("actor.type = %q, want %q", wire.Actor.Type, env.Actor.Type)
	}
	if wire.Entity.Type != env.Entity.Type || ids.UUID(wire.Entity.Id) != env.Entity.ID {
		t.Errorf("entity = %+v, want type=%q id=%v", wire.Entity, env.Entity.Type, env.Entity.ID)
	}
	if ids.UUID(wire.CorrelationId) != env.Trace.CorrelationID {
		t.Errorf("correlation_id = %v, want %v", wire.CorrelationId, env.Trace.CorrelationID)
	}
	if got := wire.Data["stage_id"]; got != "s-1" {
		t.Errorf("data.stage_id = %v, want the typed payload's value \"s-1\"", got)
	}
	if got := wire.Data["amount"]; got != float64(42) {
		t.Errorf("data.amount = %v, want the typed payload's value 42", got)
	}
}

// TestToWireEnvelopeEmptyPayloadYieldsEmptyObject covers an envelope with no
// payload set at all (the zero json.RawMessage, as the old lead.created /
// *.archived sites emitted) — it must not panic or error, and data must be
// an empty object {}, never JSON null: the public PublicEventEnvelope.data
// is a required object and a null would fail a subscriber's validation.
func TestToWireEnvelopeEmptyPayloadYieldsEmptyObject(t *testing.T) {
	env := fullInternalEnvelope(t)
	env.Payload = nil
	wire, err := toWireEnvelope(env)
	if err != nil {
		t.Fatalf("toWireEnvelope with no payload: %v", err)
	}
	if wire.Data == nil {
		t.Fatal("data = nil, want an empty object {} for an envelope with no payload")
	}
	if len(wire.Data) != 0 {
		t.Errorf("data = %v, want an empty object {}", wire.Data)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshaling the wire envelope: %v", err)
	}
	if !strings.Contains(string(body), `"data":{}`) {
		t.Errorf("delivered body must carry data:{}, not null: %s", body)
	}
}

// TestToWireEnvelopeRejectsUndecodablePayload proves a payload that is not
// valid JSON is surfaced as an error rather than silently producing an empty
// or truncated data field — env.Payload is expected to always be JSON this
// same process marshaled, so reaching this path is itself a defect worth
// failing loudly on.
func TestToWireEnvelopeRejectsUndecodablePayload(t *testing.T) {
	env := fullInternalEnvelope(t)
	env.Payload = json.RawMessage(`{not valid json`)
	if _, err := toWireEnvelope(env); err == nil {
		t.Fatal("toWireEnvelope with an undecodable payload must error, not silently proceed")
	}
}

// TestFrozenReplayResendsTheStoredBodyVerbatim pins the frozen-replay
// guarantee this task must preserve: toWireEnvelope applies only when a
// delivery is freshly enqueued. A delivery already parked with a
// pre-migration (internal-shape) stored body must replay that body
// UNCHANGED — attempt() never re-derives or re-wraps t.payload, it only
// signs and sends the bytes already on the row.
func TestFrozenReplayResendsTheStoredBodyVerbatim(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

	cipher, err := NewCipher(make([]byte, WebhookKeyBytes))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	secret, err := generateSecret()
	if err != nil {
		t.Fatalf("generating secret: %v", err)
	}
	sealed, err := cipher.seal(secret)
	if err != nil {
		t.Fatalf("sealing secret: %v", err)
	}

	// preMigrationBody is what an old, already-parked row's payload column
	// holds: the RAW internal kevents.Envelope shape (audit_log_id and all),
	// marshaled before toWireEnvelope existed.
	preMigrationBody, err := json.Marshal(fullInternalEnvelope(t))
	if err != nil {
		t.Fatalf("marshaling the pre-migration internal envelope: %v", err)
	}

	var gotBody []byte
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	d := NewDeliverer(NewStore(nil, cipher), receiver.Client(), clock, nil, log)
	target := attemptTarget{
		deliveryID:   ids.NewV7(),
		subID:        ids.NewV7(),
		targetURL:    receiver.URL,
		sealedSecret: sealed,
		eventType:    "deal.created",
		eventID:      ids.NewV7(),
		payload:      preMigrationBody,
	}
	out := d.attempt(context.Background(), target)
	if out.failure != "" {
		t.Fatalf("attempt failed: %s", out.failure)
	}
	if string(gotBody) != string(preMigrationBody) {
		t.Fatalf("replay altered the stored body:\n got  %s\n want %s (verbatim pre-migration shape)", gotBody, preMigrationBody)
	}
	// Confirm this genuinely is the OLD internal shape (not accidentally the
	// public one) — the frozen-replay guarantee is only meaningful if the
	// stored body actually still carries the internal fields.
	if !strings.Contains(string(gotBody), "audit_log_id") {
		t.Fatal("test setup error: preMigrationBody does not carry the internal shape it is meant to pin")
	}
}
