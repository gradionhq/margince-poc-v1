// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package aiactivity

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// aiTaskStateChanged is the generated payload's zero value, DECLARED rather
// than composed on purpose: a composite literal of a payload struct is how the
// event-ownership gate finds a module that PUBLISHES a type, and this consumer
// publishes nothing. Composing one here would file the projection as a second
// emitter of the event it exists to receive.
var aiTaskStateChanged crmcontracts.InternalEventAiTaskStateChanged

// eventType is the one type this consumer projects. Taken from the generated
// payload rather than written out, so the string cannot drift from the schema.
var eventType = aiTaskStateChanged.EventType()

// Consumer projects ai_task.state_changed into ai_task_run.
type Consumer struct {
	store *Store
	log   *slog.Logger
}

// NewConsumer builds the projection's bus handler.
func NewConsumer(store *Store, log *slog.Logger) *Consumer {
	return &Consumer{store: store, log: log}
}

// HandleEvent projects one ai_task.state_changed.
//
// An event this consumer does not care about answers nil, so the group keeps
// flowing rather than wedging on somebody else's traffic — the same contract
// every other consumer in the tree honours.
func (c *Consumer) HandleEvent(ctx context.Context, env events.Envelope) error {
	if env.Type != eventType {
		return nil
	}
	// A payload that does not decode and an actor that does not parse are both
	// DETERMINISTIC refusals: the same bytes will refuse identically on every
	// redelivery. Returning an error would leave the entry pending, and the
	// reclaim pass would hand it back to every replica forever — poisoning a
	// lane whose whole job is to keep a display current. So they are logged at
	// ERROR and acked away, the same disposition the subscriber itself takes
	// for an undecodable envelope. Only a DATABASE failure is returned below,
	// because that one can succeed on the next attempt.
	var p crmcontracts.InternalEventAiTaskStateChanged
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		c.log.Error("aiactivity: dropping an event whose payload does not decode",
			"event_id", env.EventID, "type", env.Type, "error", err)
		return nil
	}
	scope, user, err := ResolveActor(env.Actor)
	if err != nil {
		c.log.Error("aiactivity: dropping an event whose actor cannot be attributed",
			"event_id", env.EventID, "source", p.Source, "occurrence_key", p.OccurrenceKey, "error", err)
		return nil
	}

	applied, err := c.store.ApplyStateChange(c.projectionContext(ctx, env), change(p, scope, user, env))
	if err != nil {
		return err
	}
	if !applied {
		// The guard refused it as stale. The event WAS delivered and correctly
		// ignored, so ACKing it is right; it is logged because a run of these
		// is what a lagging emitter looks like from here.
		c.log.Debug("aiactivity: a state change the occurrence has already moved past",
			"source", p.Source, "occurrence_key", p.OccurrenceKey, "attempt", p.Attempt, "state", p.State)
	}
	return nil
}

// projectionContext binds a system principal and the event's own correlation
// id. The workspace is the HANDLE's, not the envelope's: this consumer is wired
// for one installation, and the envelope carries no tenant.
func (c *Consumer) projectionContext(ctx context.Context, env events.Envelope) context.Context {
	ctx = principal.WithCorrelationID(ctx, env.Trace.CorrelationID)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:ai_activity",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
}

// change maps one decoded payload onto the projection's write.
//
// staleAfter is derived HERE and stored, rather than recomputed by every later
// reader: the lease belongs to the source, only the source knows it, and a
// reader that had to reconstruct it would be one refactor away from rendering a
// dead occurrence as live.
func change(p crmcontracts.InternalEventAiTaskStateChanged, scope string, user ids.UUID, env events.Envelope) Change {
	return Change{
		Source:        p.Source,
		OccurrenceKey: p.OccurrenceKey,
		Kind:          p.Kind,
		AITask:        deref(p.AiTask),
		Attempt:       p.Attempt,
		ActorScope:    scope,
		ActorUserID:   user,
		PassportID:    derefID(env.Actor.PassportID),
		State:         p.State,
		QueuedAt:      p.QueuedAt,
		StartedAt:     p.StartedAt,
		FinishedAt:    p.FinishedAt,
		StaleAfter:    staleAfter(p),
		SubjectType:   deref(p.SubjectType),
		SubjectID:     derefContractID(p.SubjectId),
		Quantity:      p.Quantity,
		QuantityUnit:  deref(p.QuantityUnit),
		DegradeReason: deref(p.DegradeReason),
		Summary:       deref(p.Summary),
		EventID:       env.EventID,
	}
}

// staleAfter is when a LIVE attempt stops being believable: the instant this
// attempt became current, plus the source's own lease. A settled occurrence has
// none — it is not claiming to be working — and neither does a source that
// declares no lease, which the read renders as live for as long as the source
// says nothing.
func staleAfter(p crmcontracts.InternalEventAiTaskStateChanged) *time.Time {
	if p.LeaseSeconds == nil || *p.LeaseSeconds <= 0 {
		return nil
	}
	if p.State != "queued" && p.State != "running" {
		return nil
	}
	// A RUNNING attempt ages from its claim; a queued one ages from when it was
	// queued, whatever else the payload carries. Reading started_at whenever it
	// is present would let one emitter's leftover claim timestamp mark a
	// freshly queued attempt stale on arrival — and the source that stopped
	// clearing it would have no way to say so.
	from := p.QueuedAt
	if p.State == "running" && p.StartedAt != nil {
		from = *p.StartedAt
	}
	out := from.Add(time.Duration(*p.LeaseSeconds) * time.Second)
	return &out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefID(id *ids.UUID) ids.UUID {
	if id == nil {
		return ids.Nil
	}
	return *id
}

// derefContractID crosses the one type boundary between the generated payload
// and the kernel: both are [16]byte, and the generator names the contract's
// spelling.
func derefContractID(id *openapi_types.UUID) ids.UUID {
	if id == nil {
		return ids.Nil
	}
	return ids.UUID(*id)
}
