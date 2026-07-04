// Package events is the wire contract of the gw:events bus: the standard
// envelope (events.md §2), the <entity>.<verb> catalog (§1/§5), and the
// per-entity-type stream layout (§4.1). It is part of the dependency-free
// kernel so both the publishing side (crm-core's outbox writes) and the
// consuming side (the relay and subscriber in internal/bus) share one
// shape without either importing the other.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gradionhq/fable-poc/kernel/ids"
)

// Envelope is the events.md §2 shape every bus entry carries. Payload is
// the only per-type-varying field and stays raw JSON here: publishers
// marshal their typed payload in, consumers decode into the type the
// catalog names for the event's `type`+`version`.
type Envelope struct {
	// EventID is minted as UUIDv7 so it is time-ordered; it is the
	// consumer-side idempotency key (§3 — dedupe on event_id).
	EventID     ids.UUID        `json:"event_id"`
	Type        string          `json:"type"`
	Version     int             `json:"version"`
	WorkspaceID ids.UUID        `json:"workspace_id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Actor       Actor           `json:"actor"`
	Entity      EntityRef       `json:"entity"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Trace       Trace           `json:"trace"`
}

// Actor answers "who did this, under whose authority" from the event
// alone — the structured mirror of the audit_log actor columns
// (data-model §11).
type Actor struct {
	Type string `json:"type"` // human | agent | connector | system
	ID   string `json:"id"`   // "human:<uuid>" | "agent:<id>" | "connector:<name>" | "system"
	// PassportID is the Agent Seat Passport that authorized an agent
	// action; nil for humans. OnBehalfOf is the human authority behind an
	// agent/connector action.
	PassportID *ids.UUID `json:"passport_id"`
	OnBehalfOf *ids.UUID `json:"on_behalf_of"`
}

// EntityRef names the subject entity — a ref, never the body (§0: a
// consumer that needs the record reads it back under its own scopes).
type EntityRef struct {
	Type string   `json:"type"`
	ID   ids.UUID `json:"id"`
}

// Trace lets a consumer reconstruct the causal chain of one originating
// request / agent run / capture batch as a single story (§2).
type Trace struct {
	// CorrelationID groups every event of one originating operation.
	CorrelationID ids.UUID `json:"correlation_id"`
	// CausationID is the event_id that caused THIS event; nil for the
	// first event in a chain.
	CausationID *ids.UUID `json:"causation_id"`
	// AuditLogID is the audit_log row written in the same transaction.
	AuditLogID ids.UUID `json:"audit_log_id"`
}

// Validate rejects an envelope that would be unroutable or unauditable on
// the bus. It is the shared gate for both directions: the publisher runs
// it before staging an outbox row, the subscriber before dispatching to a
// handler (a malformed entry must fail loudly, not corrupt a consumer).
func (e Envelope) Validate() error {
	if e.EventID.IsZero() {
		return errors.New("events: envelope has no event_id")
	}
	if _, err := StreamFor(e.Type); err != nil {
		return err
	}
	if v := VersionOf(e.Type); e.Version != v {
		return fmt.Errorf("events: %s is at version %d, envelope says %d", e.Type, v, e.Version)
	}
	if e.WorkspaceID.IsZero() {
		return fmt.Errorf("events: %s envelope has no workspace_id (the bus analogue of RLS)", e.Type)
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("events: %s envelope has no occurred_at", e.Type)
	}
	switch e.Actor.Type {
	case "human", "agent", "connector", "system":
	default:
		// The schema (audit_log CHECK) and the wire contract must agree
		// on the actor classes — a fifth class slipping onto the bus
		// would break the audit mirror silently.
		return fmt.Errorf("events: %s envelope has unknown actor type %q", e.Type, e.Actor.Type)
	}
	if e.Actor.ID == "" {
		return fmt.Errorf("events: %s envelope has no actor", e.Type)
	}
	if e.Entity.Type == "" || e.Entity.ID.IsZero() {
		return fmt.Errorf("events: %s envelope has no entity ref", e.Type)
	}
	if e.Trace.CorrelationID.IsZero() || e.Trace.AuditLogID.IsZero() {
		return fmt.Errorf("events: %s envelope has an incomplete trace", e.Type)
	}
	return nil
}
