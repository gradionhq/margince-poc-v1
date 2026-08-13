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

// NewPersonDataEnrich builds the consumer.
//
// The worker does not start this lane at all without a registry and a vault
// (cmd/worker/persondataenrich.go), so a nil run service is not a deployment
// posture — it is a wiring mistake. The nil check in HandleEvent is a
// defensive floor rather than a supported configuration: it answers nil
// instead of panicking on a bus message, because a consumer that crashes the
// group is worse than one that quietly does nothing, and the missing lane is
// visible at boot where it belongs.
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
	trigger, admitted := triggerFor(env.Actor)
	if !admitted {
		return nil
	}
	// The envelope carries no tenant (ADR-0091 §6); the store's handle names it.
	ws, err := InstallationDB(g.pool).Workspace(ctx)
	if err != nil {
		return err
	}
	_, err = g.runs.QueueRun(g.systemContext(ctx, env, ws.UUID), provider.QueueInput{
		PersonID: env.Entity.ID.String(),
		Trigger:  trigger,
	})
	return g.swallowConfiguration(err)
}

// triggerFor decides which toggle this arrival is governed by, from WHO
// created the person. The trigger is not a property of the consumer: the same
// person.created event is emitted by four writers, and the customer set
// different policies for them.
//
//   - A HUMAN typing a contact is the individual create the
//     automatic_individual_create toggle is about.
//   - A CONNECTOR is capture: a mailbox sync creates a person per external
//     counterparty, and connecting a mailbox with a year of history would
//     otherwise buy thousands of records under a toggle that says
//     "individual". That is what automatic_import governs, and it defaults
//     OFF with a preview and an estimate behind it (PI-PARAM-2).
//   - An AGENT gets nothing. Buying provider data is human-only on the REST
//     surface (x-agent-access: human-only, ADR-0055), and an agent that can
//     create a person must not reach through this event what the policy
//     denies it at the door — the seat cap is what the agent's human could do
//     through the same gate, and there is no gate here.
//   - A SYSTEM actor is this platform's own writer (merge survivorship,
//     backfills). It has no human intent behind it, so it buys nothing.
func triggerFor(actor events.Actor) (provider.Trigger, bool) {
	switch actor.Type {
	case "human":
		return provider.TriggerAutomaticCreate, true
	case "connector":
		return provider.TriggerAutomaticImport, true
	default:
		return "", false
	}
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
