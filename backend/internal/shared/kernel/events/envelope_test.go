// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package events

import (
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// validEnvelope builds a fully-populated envelope for the given type — the
// baseline a test then mutates one field of to probe a single Validate rule.
func validEnvelope(t *testing.T, eventType string) Envelope {
	t.Helper()
	return Envelope{
		EventID:     ids.NewV7(),
		Type:        eventType,
		Version:     VersionOf(eventType),
		WorkspaceID: ids.NewV7(),
		OccurredAt:  time.Now().UTC(),
		Actor:       Actor{Type: "connector", ID: "connector:gmail"},
		Entity:      EntityRef{Type: "activity", ID: ids.NewV7()},
		Trace:       Trace{CorrelationID: ids.NewV7(), AuditLogID: ids.NewV7()},
	}
}

// A pipeline event (capture.skipped) is subject-less by nature — an excluded
// message creates NOTHING — yet the spec requires it on the bus as the AC1.3
// proof. Validate must accept it with an empty Entity.
func TestValidate_pipelineEventAllowsEmptyEntity(t *testing.T) {
	env := validEnvelope(t, "capture.skipped")
	env.Entity = EntityRef{}
	if err := env.Validate(); err != nil {
		t.Fatalf("capture.skipped with empty entity should validate, got: %v", err)
	}
}

// Every non-pipeline event still names a subject: a consumer reads it back
// under its own RLS and routes on the entity type. Empty entity must fail.
func TestValidate_nonPipelineEventRequiresEntity(t *testing.T) {
	env := validEnvelope(t, "person.created")
	env.Entity = EntityRef{}
	if err := env.Validate(); err == nil {
		t.Fatal("person.created with empty entity must be rejected")
	}
}

// Relaxing the entity ref must NOT drop the ledger trace link: a pipeline
// event still carries its ledger row id (audit_log OR system_log) so the
// outcome stays attributable.
func TestValidate_pipelineEventStillRequiresLedgerTrace(t *testing.T) {
	env := validEnvelope(t, "capture.skipped")
	env.Entity = EntityRef{}
	env.Trace.AuditLogID = ids.Nil
	if err := env.Validate(); err == nil {
		t.Fatal("a pipeline event with no ledger row id must be rejected (incomplete trace)")
	}
}

// A pipeline event MAY still carry an entity where one exists
// (capture.received names the raw record) — the relaxation makes the entity
// optional, it does not forbid it.
func TestValidate_pipelineEventMayCarryEntity(t *testing.T) {
	env := validEnvelope(t, "capture.received")
	if err := env.Validate(); err != nil {
		t.Fatalf("capture.received with an entity should validate, got: %v", err)
	}
}
