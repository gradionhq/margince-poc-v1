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
	"fmt"
	"maps"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/provenance"
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

	beforeJSON, err := marshalOrNil(before)
	if err != nil {
		return ids.Nil, err
	}
	afterJSON, err := marshalOrNil(after)
	if err != nil {
		return ids.Nil, err
	}
	evidence, err = withExtensionAttribution(ctx, evidence)
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
		// The tenant comes from the TRANSACTION's binding, not from the caller:
		// the ledger row must name the workspace its domain row landed in, and
		// the GUC is the one thing that decides both. Read from ctx they could
		// disagree, and the disagreement would be invisible.
		`INSERT INTO audit_log (id, workspace_id, actor_type, actor_id, passport_id, on_behalf_of, action, entity_type, entity_id, before, after, evidence, authorization_rule)
		 VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		id, string(p.Type), p.ID, UUIDOrNil(p.PassportID), UUIDOrNil(p.OnBehalfOf),
		action, entityType, entityID, beforeJSON, afterJSON, evidenceJSON,
		auth.AuthzRule(p, entityType, action))
	return id, err
}

// withExtensionAttribution adds the bound extension attribution to a write's
// evidence, and refuses a caller that tried to write the reserved member
// itself.
//
// It is read from the CONTEXT rather than passed down because the alternative
// is an evidence parameter threaded through every core write path — hundreds of
// call sites, all of which would have to keep passing it — and a parameter that
// every site must remember is one a new site forgets. Resolving it here is how
// Audit already resolves the actor and the workspace, so attribution follows a
// core write wherever it happens, including inside a tx-accepting seam a unit
// reached through the port.
//
// The refusal is the whole reason this returns an error. `extension` is a
// core-stamped member: a caller supplying one would either be overwritten
// (losing what it meant to record) or would overwrite the stamp (claiming an
// attribution it does not have), and both of those are silent. A fitness test
// asserts no core module writes the key.
//
//craft:ignore naked-any the audit evidence seam is jsonb; its members are each writer's own shape
func withExtensionAttribution(ctx context.Context, evidence map[string]any) (map[string]any, error) {
	if _, taken := evidence[provenance.ExtensionEvidenceKey]; taken {
		return nil, fmt.Errorf("store: audit evidence key %q is stamped by the core from the invocation, so a caller may not supply it",
			provenance.ExtensionEvidenceKey)
	}
	ext, ok := provenance.ExtensionFrom(ctx)
	if !ok {
		return evidence, nil
	}
	merged := make(map[string]any, len(evidence)+1)
	maps.Copy(merged, evidence)
	merged[provenance.ExtensionEvidenceKey] = ext.EvidenceEntry()
	return merged, nil
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
	id := ids.NewV7()
	_, err = tx.Exec(ctx,
		// The tenant is the transaction's, like every other ledger row here.
		`INSERT INTO system_log (id, workspace_id, actor_type, actor_id, passport_id, on_behalf_of, action, detail)
		 VALUES ($1, NULLIF(current_setting('app.workspace_id', true), '')::uuid, $2, $3, $4, $5, $6, $7)`,
		id, string(p.Type), p.ID, UUIDOrNil(p.PassportID), UUIDOrNil(p.OnBehalfOf),
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
	correlationID, ok := principal.CorrelationID(ctx)
	if !ok {
		// Every write path opens an operation scope (the HTTP middleware,
		// a consumer re-binding its trigger); a missing one is a
		// programming error, caught before the row hits the events.
		return errors.New("store: no correlation id bound to context")
	}

	env := events.Envelope{
		EventID:    ids.NewV7(),
		Type:       eventType,
		Version:    events.VersionOf(eventType),
		OccurredAt: time.Now().UTC(),
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

// EmitEvent is Emit with the event type and entity type derived FROM the
// payload (p.EventType(), p.EntityType()) instead of taken as separate
// string parameters — a wrong payload for an event, or a payload staged
// under the wrong entity type, is impossible to express at the call site.
// Use this for every event whose entity is the payload's own static
// subject (the common case); use EmitEventForEntity for the handful of
// dynamic-entity types below.
func EmitEvent(ctx context.Context, tx pgx.Tx, auditID ids.UUID, entityID ids.UUID, p events.Payload) error {
	// A dynamic-entity payload (contract `x-entity-type: dynamic`, EntityType()
	// == "dynamic") has no static subject — its runtime entity type must be
	// supplied through EmitEventForEntity. Staging one here would stamp the
	// literal "dynamic" as the entity type, which no visibility gate can route,
	// so the webhook fan-out would silently never deliver it. Reject at the
	// seam rather than emit a mislabeled envelope.
	if p.EntityType() == "dynamic" {
		return fmt.Errorf("store: %s is a dynamic-entity event — use EmitEventForEntity with its runtime entity type, not EmitEvent", p.EventType())
	}
	return Emit(ctx, tx, auditID, p.EventType(), p.EntityType(), entityID, p)
}

// EmitEventForEntity is EmitEvent for the dynamic-entity event types
// (mirror.*, consent.changed, retention.applied — contract
// `x-entity-type: dynamic`) whose subject is a runtime value the caller
// resolves, not the payload's static type: it takes entityType as a
// parameter and ignores p.EntityType() entirely.
func EmitEventForEntity(ctx context.Context, tx pgx.Tx, auditID ids.UUID, entityType string, entityID ids.UUID, p events.Payload) error {
	// This overriding path exists ONLY for dynamic-entity payloads (contract
	// `x-entity-type: dynamic`), whose subject is a runtime value. Relabeling a
	// STATIC payload here — stamping it with an entity type that is not the
	// payload's own — would break the payload/entity pairing EmitEvent
	// guarantees and could misroute or silently drop the webhook, so refuse a
	// non-dynamic payload rather than honor an arbitrary relabel.
	if p.EntityType() != "dynamic" {
		return fmt.Errorf("store: %s is a static-entity event (entity type %q) — use EmitEvent, EmitEventForEntity is for dynamic-entity payloads only", p.EventType(), p.EntityType())
	}
	return Emit(ctx, tx, auditID, p.EventType(), entityType, entityID, p)
}

// EmitPipeline stages an entity-less pipeline event (events envelope
// pipeline class — capture.skipped and its siblings): a bus event that names
// NO subject because the pipeline step produced nothing (an excluded message
// creates nothing). ledgerID is the system_log (or audit_log) row written in
// the SAME transaction — the trace link that keeps the event attributable.
// It is Emit with an empty entity ref; Validate admits an empty entity only
// for the pipeline-event types, so a caller that hands a normal event type
// here is refused rather than silently shipping an unroutable envelope.
//
//craft:ignore naked-any the outbox payload seam: each event type carries its own events.md payload shape, serialized into the envelope (same as Emit)
func EmitPipeline(ctx context.Context, tx pgx.Tx, ledgerID ids.UUID, eventType string, payload any) error {
	if !events.IsPipelineEvent(eventType) {
		return fmt.Errorf("store: %s is not an entity-less pipeline event — use Emit with its entity ref", eventType)
	}
	return Emit(ctx, tx, ledgerID, eventType, "", ids.Nil, payload)
}

// UUIDOrNil maps a zero UUID to SQL NULL / JSON null (the Principal uses
// the zero value for "not an agent action").
func UUIDOrNil(id ids.UUID) *ids.UUID {
	if id.IsZero() {
		return nil
	}
	return &id
}

// MustWorkspace is the workspace the caller's context names, for the domain
// INSERTs that still stamp a `workspace_id` column of their own.
//
// The ledger writes above no longer use it: they take the tenant from the
// TRANSACTION's binding, which is the only thing that can agree with the
// domain row by construction. These callers follow when the column itself
// goes (ADR-0091 §8 phase D).
func MustWorkspace(ctx context.Context) ids.UUID {
	wsID, _ := principal.WorkspaceID(ctx)
	return wsID
}

// LockWriteIdentity serializes every writer of one logical record identity
// for the transaction (workspace-scoped pg advisory xact lock). A
// precondition read and its dependent write must both run under it — READ
// COMMITTED alone leaves a window where a concurrent standalone write
// commits between the two statements and is silently overwritten. The lock
// is reentrant within one transaction, so a caller that locked at its
// precondition read may write through the same store path without deadlock.
func LockWriteIdentity(ctx context.Context, tx pgx.Tx, entityType, identity string) error {
	// The workspace in the key is the TRANSACTION's, read where the lock is
	// taken: a key built from a ctx that disagreed with the binding would put
	// two writers of one record on different locks and serialize neither.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
		$1 || ':' || coalesce(current_setting('app.workspace_id', true), '') || ':' || $2, 0))`,
		entityType+"_write", identity); err != nil {
		return fmt.Errorf("lock %s write identity: %w", entityType, err)
	}
	return nil
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

// marshalOrNil renders one audit image, answering nil bytes — and so SQL NULL
// in the column — for an absent one.
//
// "Absent" has to include a nil MAP, not just an untyped nil, and that is the
// whole reason this is not a bare json.Marshal. A caller that builds its image
// in a `map[string]any` and leaves it nil ("there was no prior state") hands
// this an interface holding a typed nil, which `v == nil` reads as present:
// json.Marshal then writes the four bytes `null`, and every "there was no prior
// state" query — `WHERE before IS NULL`, the shape the rest of the tree uses —
// silently misses the row. The image is absent either way; only the column
// stops saying so.
//
//craft:ignore naked-any marshals the audit seam's schemaless before/after images (see Audit)
func marshalOrNil(v any) ([]byte, error) {
	if v == nil || isNilValue(v) {
		return nil, nil
	}
	return json.Marshal(v)
}

// isNilValue reports whether v carries a typed nil of a kind that can be one.
// Kinds that cannot be are answered false without inspecting their contents,
// so a zero struct or an empty map stays an image. reflect.Interface is absent
// deliberately: ValueOf resolves to the dynamic type, so an interface kind
// never reaches here.
//
//craft:ignore naked-any the same audit-seam value marshalOrNil inspects
func isNilValue(v any) bool {
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer:
		return rv.IsNil()
	default:
		return false
	}
}
