// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Automatic enrichment on create (PI-EVT-1): a person who arrives gets their
// data bought from the connected provider, if the customer switched that on.
//
// THE TRIGGER IS THE EVENT, NOT THE WRITER — the same shape personautoenrich
// uses, and for the same reason. person.created reaches the outbox because the
// write shape puts it there, so manual entry, capture, import and the site
// read all land here without any of them knowing this consumer exists.
//
// It only QUEUES. The run is admitted, fenced, frozen and reserved inside
// QueueRun's transaction, and the provider is called later by the worker, so
// a slow vendor can never hold up the event lane and a refusal costs nothing.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/integrations"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// personDataEnrichActor is the provenance a run queued by this pass carries:
// distinct from the site-read auto-enrich actor, so a reader can tell a value
// this installation PAID for from one it read off a public page.
const personDataEnrichActor = "system:person-data-enrich"

// The event this consumer acts on, and the object its principal is granted.
// Spelled here rather than reusing flipObjectPerson, which names the same
// word for the incumbent-flip surface: two unrelated features sharing one
// constant is how a rename in either silently changes the other.
const (
	personCreatedEvent = "person.created"
	personObject       = "person"
)

// PersonDataEnrich queues a provider run for each newly created person.
type PersonDataEnrich struct {
	pool *pgxpool.Pool
	runs provider.RunService
	log  *slog.Logger
}

// NewPersonDataEnrich builds the consumer. A nil run service is a deployment
// with no provider configured: HandleEvent then answers nil for every event
// rather than erroring, because "no provider connected" is a supported
// configuration and not a fault (PI-AC-9).
func NewPersonDataEnrich(pool *pgxpool.Pool, runs provider.RunService, log *slog.Logger) *PersonDataEnrich {
	return &PersonDataEnrich{pool: pool, runs: runs, log: log}
}

// HandleEvent routes one envelope. Only person.created: this is the
// automatic_create trigger, and the other person events are edits to a record
// whose data was already bought or deliberately not bought. A re-enrichment
// on update would spend the customer's credits every time somebody fixed a
// typo.
//
// Redelivery is free: the bus is at-least-once, and QueueRun's live-run index
// returns the existing run rather than buying the same answer twice.
func (g *PersonDataEnrich) HandleEvent(ctx context.Context, env events.Envelope) error {
	if g.runs == nil || env.Type != personCreatedEvent {
		return nil
	}
	if env.Entity.ID == ids.Nil || env.Entity.Type != string(recordTypePerson) {
		return nil
	}
	// The envelope carries no tenant (ADR-0091 §6); the store's handle names it.
	ws, err := InstallationDB(g.pool).Workspace(ctx)
	if err != nil {
		return err
	}
	_, err = g.runs.QueueRun(g.systemContext(ctx, env, ws.UUID), provider.QueueInput{
		PersonID: env.Entity.ID.String(),
		Trigger:  provider.TriggerAutomaticCreate,
	})
	return g.swallowConfiguration(err)
}

// swallowConfiguration turns the two "this is the configuration working"
// refusals into success. Auto-enrich switched off and no provider connected
// are both states a customer chose; logging them as failures would fill the
// worker log with noise on every person created in a workspace that never
// wanted this.
//
// A typed predicate, never error text: integrations.IsTriggerNotAdmitted is
// there so the sentinel can be reworded without silently turning a swallowed
// state into a logged error, or the reverse.
func (g *PersonDataEnrich) swallowConfiguration(err error) error {
	switch {
	case err == nil:
		return nil
	case integrations.IsTriggerNotAdmitted(err):
		return nil
	case errors.Is(err, provider.ErrNotConnected):
		return nil
	default:
		return err
	}
}

// systemContext binds the workspace and the system principal this pass runs
// under. Queueing a run is gated on seeing the subject, and this pass acts for
// the installation rather than for a person, so it carries the system
// principal's full row scope; the correlation id carries through, so a
// purchase traces back to the event that caused it.
func (g *PersonDataEnrich) systemContext(ctx context.Context, env events.Envelope, ws ids.UUID) context.Context {
	ctx = principal.WithWorkspaceID(ctx, ws)
	ctx = principal.WithCorrelationID(ctx, env.Trace.CorrelationID)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: personDataEnrichActor,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{personObject: {Read: true}},
			RowScope: principal.RowScopeAll,
		},
	})
}
