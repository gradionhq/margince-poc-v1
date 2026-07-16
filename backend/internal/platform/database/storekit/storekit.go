// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package storekit is the shared store mechanics under every module's
// persistence layer (ADR-0054 §6): the one non-negotiable write shape
// (data-model §11, events.md §4.2 — domain row + audit_log row +
// event_outbox row commit in ONE transaction), keyset pagination,
// optimistic-version patches, and the SQLSTATE branch helpers. Modules
// own their tables and SQL; the invariants live here, spelled once.
package storekit

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Actor resolves the audit identity of the current call. A missing actor
// is a programming error (the middleware always binds one).
func Actor(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.Actor(ctx)
	if !ok {
		return principal.Principal{}, errors.New("store: no actor bound to context")
	}
	return p, nil
}

// CapturedBy is the server-derived provenance stamp: always the
// authenticated principal, never a client-supplied string (a client that
// could write captured_by could forge the P5 provenance signal).
func CapturedBy(ctx context.Context) (string, error) {
	p, err := Actor(ctx)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// Audit writes the append-only audit_log row inside the mutation's
// transaction — atomic with the domain write by construction — and
// returns the row's id so the paired event can carry it as
// trace.audit_log_id (events.md §2).
//
//craft:ignore naked-any the audit seam: before/after images are each entity's own snapshot shape, serialized to jsonb
func Audit(ctx context.Context, tx pgx.Tx, action, entityType string, entityID ids.UUID, before, after any) (ids.UUID, error) {
	return AuditWithEvidence(ctx, tx, action, entityType, entityID, before, after, nil)
}

// AuditWithEvidence is Audit for a write that carries operational
// evidence — context ABOUT the mutation (which retention policy fired,
// which inbound message triggered it), landing in audit_log.evidence.
// before/after stay reserved for the record's own field images: a
// writer that folds operation metadata into them makes downstream
// projections (field history) read it as field changes that never
// happened on the record.
//
//craft:ignore naked-any the audit seam: before/after images are each entity's own snapshot shape, serialized to jsonb
func AuditWithEvidence(ctx context.Context, tx pgx.Tx, action, entityType string, entityID ids.UUID, before, after any, evidence map[string]any) (ids.UUID, error) {
	p, err := Actor(ctx)
	if err != nil {
		return ids.Nil, err
	}
	wsID, _ := principal.WorkspaceID(ctx)

	beforeJSON, err := marshalOrNil(before)
	if err != nil {
		return ids.Nil, err
	}
	afterJSON, err := marshalOrNil(after)
	if err != nil {
		return ids.Nil, err
	}
	var evidenceJSON []byte
	if evidence != nil {
		evidenceJSON, err = json.Marshal(evidence)
		if err != nil {
			return ids.Nil, err
		}
	}

	id := ids.NewV7()
	_, err = tx.Exec(ctx,
		`INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, passport_id, on_behalf_of, action, entity_type, entity_id, before, after, evidence, authorization_rule)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		id, wsID, string(p.Type), p.ID, UUIDOrNil(p.PassportID), UUIDOrNil(p.OnBehalfOf),
		action, entityType, entityID, beforeJSON, afterJSON, evidenceJSON,
		auth.AuthzRule(p, entityType, action))
	return id, err
}

// LogSystem writes one append-only system_log row inside the current
// transaction — the ledger for a SYSTEM / non-entity operational event
// (login, bulk export, capture skip) that mutates no record and so has no
// place in audit_log (the P12 record-mutation spine). Actor and workspace
// are derived exactly as Audit derives them — from the authenticated
// principal and the workspace GUC — so a caller with no actor bound is a
// programming error, refused before any SQL runs. It returns the row id so
// an entity-less pipeline event can carry it as trace.audit_log_id (the
// repurposed "ledger row id", events.md §2). detail is nil-safe: nil writes
// SQL NULL.
func LogSystem(ctx context.Context, tx pgx.Tx, action string, detail map[string]any) (ids.UUID, error) {
	p, err := Actor(ctx)
	if err != nil {
		return ids.Nil, err
	}
	// MustWorkspace is safe here: LogSystem only runs inside WithWorkspaceTx,
	// which already failed if no workspace was bound, and the system_log RLS
	// WITH CHECK rejects a mismatched workspace_id as a final backstop.
	wsID := MustWorkspace(ctx)

	id := ids.NewV7()
	_, err = tx.Exec(ctx,
		`INSERT INTO system_log (id, workspace_id, actor_type, actor_id, passport_id, on_behalf_of, action, detail)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, wsID, string(p.Type), p.ID, UUIDOrNil(p.PassportID), UUIDOrNil(p.OnBehalfOf),
		action, JSONArg(detail))
	return id, err
}

// Emit stages a domain event in the transactional outbox (events.md
// §4.2). The envelope is complete at staging time — event_id (UUIDv7),
// actor incl. passport/on-behalf-of, and the trace linking this event to
// its audit row, its request's correlation scope, and (for bus-derived
// writes) the causing event — so the relay ships it verbatim.
//
//craft:ignore naked-any the outbox payload seam: each event type carries its own events.md payload shape, serialized into the envelope
func Emit(ctx context.Context, tx pgx.Tx, auditID ids.UUID, eventType, entityType string, entityID ids.UUID, payload any) error {
	p, err := Actor(ctx)
	if err != nil {
		return err
	}
	wsID, _ := principal.WorkspaceID(ctx)
	correlationID, ok := principal.CorrelationID(ctx)
	if !ok {
		// Every write path opens an operation scope (the HTTP middleware,
		// a consumer re-binding its trigger); a missing one is a
		// programming error, caught before the row hits the events.
		return errors.New("store: no correlation id bound to context")
	}

	env := events.Envelope{
		EventID:     ids.NewV7(),
		Type:        eventType,
		Version:     events.VersionOf(eventType),
		WorkspaceID: wsID,
		OccurredAt:  time.Now().UTC(),
		Actor: events.Actor{
			Type:       string(p.Type),
			ID:         p.ID,
			PassportID: UUIDOrNil(p.PassportID),
			OnBehalfOf: UUIDOrNil(p.OnBehalfOf),
		},
		Entity: events.EntityRef{Type: entityType, ID: entityID},
		Trace: events.Trace{
			CorrelationID: correlationID,
			AuditLogID:    auditID,
		},
	}
	if causeID, ok := principal.CausationEvent(ctx); ok {
		env.Trace.CausationID = &causeID
	}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		env.Payload = raw
	}

	stream, err := events.StreamFor(eventType)
	if err != nil {
		return err
	}
	if err := env.Validate(); err != nil {
		return err
	}

	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO event_outbox (stream, envelope) VALUES ($1, $2)`,
		stream, body)
	return err
}

// UUIDOrNil maps a zero UUID to SQL NULL / JSON null (the Principal uses
// the zero value for "not an agent action").
func UUIDOrNil(id ids.UUID) *ids.UUID {
	if id.IsZero() {
		return nil
	}
	return &id
}

// MustWorkspace is safe inside a workspace-bound transaction:
// WithWorkspaceTx already failed if no workspace was bound.
func MustWorkspace(ctx context.Context) ids.UUID {
	wsID, _ := principal.WorkspaceID(ctx)
	return wsID
}

// JSONArg marshals a map for a jsonb parameter, passing NULL for nil.
//
//craft:ignore naked-any a jsonb bind parameter is either SQL NULL (nil) or raw bytes — pgx accepts both only as any
func JSONArg(m map[string]any) any {
	if m == nil {
		return nil
	}
	raw, _ := json.Marshal(m)
	return raw
}

//craft:ignore naked-any marshals the audit seam's schemaless before/after images (see Audit)
func marshalOrNil(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}
