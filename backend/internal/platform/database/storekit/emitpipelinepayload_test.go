// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package storekit

// EmitPipelinePayload is EmitPipeline with the event type taken from the
// payload struct instead of from a string literal at the call site. Both halves
// are tested here: the envelope it stages, and the refusal that stops an
// ordinary event type from reaching a seam that ships no entity ref.

import (
	"encoding/json"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestEmitPipelinePayloadStagesAnEntitylessEnvelopeOnThePayloadsOwnStream(t *testing.T) {
	ctx := emitTestContext()
	tx := &fakeTx{}
	ledgerID := ids.NewV7()

	err := EmitPipelinePayload(ctx, tx, ledgerID, crmcontracts.InternalEventAiTaskStateChanged{
		Source: "attachment_extraction", OccurrenceKey: "k", Kind: "document_extract",
		Attempt: 1, State: "queued",
	})
	if err != nil {
		t.Fatalf("EmitPipelinePayload: %v", err)
	}

	stream, env := decodedOutboxRow(t, tx)
	if want := events.StreamPrefix + "aitask"; stream != want {
		t.Fatalf("stream = %q, want %q", stream, want)
	}
	if env.Type != "ai_task.state_changed" {
		t.Fatalf("envelope.Type = %q, want %q", env.Type, "ai_task.state_changed")
	}
	if env.Entity.Type != "" || !env.Entity.ID.IsZero() {
		t.Fatalf("envelope.Entity = %+v, want the empty ref an entity-less event carries", env.Entity)
	}
	if env.Trace.AuditLogID != ledgerID {
		t.Fatalf("envelope.Trace.AuditLogID = %v, want the ledger row written in the same transaction %v", env.Trace.AuditLogID, ledgerID)
	}
	var decoded crmcontracts.InternalEventAiTaskStateChanged
	if err := json.Unmarshal(env.Payload, &decoded); err != nil {
		t.Fatalf("unmarshaling the staged payload: %v", err)
	}
	if decoded.OccurrenceKey != "k" || decoded.State != "queued" {
		t.Fatalf("staged payload = %+v, want the one handed in", decoded)
	}
}

// The refusal is the point: a caller who reaches for this with an ordinary
// event type would otherwise ship an envelope with no entity ref, which
// Validate rejects only at the outbox write — after the domain row is already
// in the transaction.
func TestEmitPipelinePayloadRefusesANonPipelinePayload(t *testing.T) {
	err := EmitPipelinePayload(emitTestContext(), &fakeTx{}, ids.NewV7(), crmcontracts.PublicEventDealCreated{})
	if err == nil {
		t.Fatal("expected a refusal for a non-pipeline event type")
	}
	if !strings.Contains(err.Error(), "deal.created") {
		t.Fatalf("the refusal must name the offending type, got %v", err)
	}
}
