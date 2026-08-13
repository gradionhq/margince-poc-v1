// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The core's half of extension.Subscription: which consumer group one unit's
// listener gets, and what happens when an event lands on it.
//
// The consuming machinery is the product's own — a consumer group per listener,
// XREADGROUP, the reclaim pass, Dedupe around the handler — so nothing here
// re-implements delivery. What this file owns is the translation: an envelope
// becomes a published Delivery, and the context it runs on is bound to an
// invocation that has nobody behind it.

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// systemActor is the actor id a bus delivery runs under — the same spelling
// every other system-driven pass in this layer writes, because the audit rows
// they produce are read together.
const systemActor = "system"

// ComposedSubscription is one registered listener: the declaration, plus the
// identity of the unit that declared it.
//
// The unit's name and version travel WITH the subscription because a delivery
// is attributed like every other extension-carried write — the ledger row a
// handler writes names the unit, its version, and the surface the work arrived
// on, which for a delivery is this subscription. The declaration alone cannot
// say any of that.
type ComposedSubscription struct {
	Unit    extension.Name
	Version extension.Version
	Sub     extension.Subscription
}

// GroupName is the consumer group this listener consumes on:
// `cg:ext-<unit>-<subscription>`.
//
// One group per SUBSCRIPTION rather than per unit, which is the line the core's
// own groups already draw (cg:graph-edge exists apart from cg:context-graph for
// exactly this reason): consumers on one group share a pending entry, so a
// handler that keeps failing would hold up a sibling that has nothing to do
// with it.
//
// It is derived from names that never change under a unit — renaming a
// subscription starts a fresh group, which re-reads the stream's retained
// history from the beginning, and Subscription.Name says so.
func (c ComposedSubscription) GroupName() string {
	return "cg:ext-" + string(c.Unit) + "-" + c.Sub.Name
}

// Group is the consumer group to run this listener on: its name, and the
// distinct streams its declared event types route to.
//
// Deriving the streams from the TYPES is what keeps a unit off streams it did
// not ask for. A listener that names one activity event consumes the activity
// stream and nothing else, so the events it never asked about are not even
// read, let alone filtered in process.
func (c ComposedSubscription) Group() (kevents.Group, error) {
	streams := make([]string, 0, len(c.Sub.Events))
	for _, eventType := range c.Sub.Events {
		stream, err := kevents.StreamFor(eventType)
		if err != nil {
			return kevents.Group{}, fmt.Errorf("compose: extension %q, subscription %q: %w", c.Unit, c.Sub.Name, err)
		}
		if !slices.Contains(streams, stream) {
			streams = append(streams, stream)
		}
	}
	slices.Sort(streams)
	return kevents.Group{Name: c.GroupName(), Streams: streams}, nil
}

// Handler is the bus consumer for this listener: it filters to the declared
// types, binds the delivery's authority, and runs the unit's handler under a
// Runtime that lives exactly as long as the delivery.
//
// WHAT A DELIVERY RUNS AS. There is nobody behind it, so the actor is the
// system principal — the same one every core bus consumer writes under — and
// the Runtime is minted unattended, which shuts the core port. Those two facts
// belong together: PrincipalSystem is a principal auth.Require does not check,
// so the port's refusal is the whole distance between a bus event and an
// unchecked core write. The unit's own tables stay writable, auditable and
// publishable, which is what reacting to a fact consists of.
//
// The correlation id CARRIES THROUGH from the triggering event and the event
// itself becomes the causation, so a fact, the reaction to it, and whatever the
// reaction publishes read as one story rather than three unrelated ones.
func (c ComposedSubscription) Handler(pool *pgxpool.Pool) func(context.Context, kevents.Envelope) error {
	db := InstallationDB(pool)
	return func(ctx context.Context, env kevents.Envelope) error {
		if !slices.Contains(c.Sub.Events, env.Type) {
			// The group's streams carry more than this listener asked for —
			// one activity event means the whole activity stream — so the
			// filter is ordinary, not a defence. Acked, because a delivery
			// nobody wants is delivered successfully.
			return nil
		}
		// The envelope carries no tenant (ADR-0091 §6); the installation's
		// handle names it, exactly as every other consumer resolves it.
		ws, err := db.Workspace(ctx)
		if err != nil {
			return err
		}
		ctx = principal.WithWorkspaceID(ctx, ws.UUID)
		ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: systemActor})
		ctx = principal.WithCorrelationID(ctx, env.Trace.CorrelationID)
		ctx = principal.WithCausationEvent(ctx, env.EventID)

		rt := deliveryRuntimeFor(ctx, string(c.Unit), string(c.Version),
			"subscription/"+c.Sub.Name, boundExtensionRuntime())
		defer rt.release()
		return c.Sub.Handle(ctx, rt, deliveryOf(env))
	}
}

// deliveryOf renders an envelope as the published Delivery.
//
// Ids become strings because the published surface exports no id type, and the
// entity ref is passed through as it arrived — including empty, for the core's
// entity-less pipeline events, which Delivery.Entity says a handler must check
// for rather than have invented a zero UUID here to hide.
func deliveryOf(env kevents.Envelope) extension.Delivery {
	d := extension.Delivery{
		EventID:    env.EventID.String(),
		Type:       env.Type,
		OccurredAt: env.OccurredAt,
		Payload:    env.Payload,
	}
	if env.Entity.Type != "" && !env.Entity.ID.IsZero() {
		d.Entity = extension.EntityRef{Type: env.Entity.Type, ID: env.Entity.ID.String()}
	}
	return d
}
