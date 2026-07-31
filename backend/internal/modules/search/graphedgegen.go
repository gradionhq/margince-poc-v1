// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// The cg:graph-edge consumer: what keeps CG-DDL-1 true as records change.
//
// Every handler here RECOMPUTES from the base tables rather than adjusting a
// counter. That is not a stylistic preference. The bus is at-least-once, so an
// adjustment double-counts on redelivery; and merge, archive and erasure
// correct history backwards, which an adjustment cannot express at all.
// Recomputing is idempotent by construction, so redelivery is free and
// correctness does not depend on each event arriving exactly once.
//
// The invalidation set is the part that is easy to get wrong, so it is written
// out rather than inferred:
//
//	activity.captured/updated/archived → the pairs that activity's
//	    participants imply, including the pair it USED to belong to when a
//	    human relinks it to someone else.
//	person.merged   → the source person's edges go, the target's are refolded.
//	person.archived/restored → that contact's edges are refolded.
//	ERASURE is NOT here, deliberately. It drops the edges inside its own
//	    transaction (privacy/erasure.go), because an erasure obligation that
//	    depends on an event being delivered fails silently when the bus is
//	    behind. This consumer previously listened for a `person.erased` event
//	    that no path emits, so every erasure left its edges standing.
//	user.deactivated → nothing. Reads filter through the live-member join, so
//	    a departure takes effect without rewriting a single row.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// GraphEdgeGen keeps the interaction projection current.
type GraphEdgeGen struct {
	store *Store
}

// NewGraphEdgeGen builds the projection consumer over the search store.
func NewGraphEdgeGen(store *Store) *GraphEdgeGen {
	return &GraphEdgeGen{store: store}
}

// HandleEvent routes one envelope to its recompute. An event this projection
// does not care about answers nil, so the consumer group keeps flowing rather
// than wedging on somebody else's traffic.
func (g *GraphEdgeGen) HandleEvent(ctx context.Context, env events.Envelope) error {
	entity := env.Entity.ID
	if entity == ids.Nil {
		return nil
	}
	ctx = g.projectionContext(ctx, env)

	switch env.Entity.Type {
	case entityActivity:
		return g.onActivity(ctx, env, entity)
	case entityPerson:
		return g.onPerson(ctx, env, entity)
	default:
		return nil
	}
}

// projectionContext binds the envelope's workspace and a system principal.
// The projection is maintenance, not a user action: it must fold EVERY
// interaction the base tables hold, including ones the human who happened to
// trigger the event could not read, or the edge counts would differ depending
// on who last touched the record.
func (g *GraphEdgeGen) projectionContext(ctx context.Context, env events.Envelope) context.Context {
	ctx = principal.WithWorkspaceID(ctx, env.WorkspaceID)
	ctx = principal.WithCorrelationID(ctx, env.Trace.CorrelationID)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:graph_edge",
		Permissions: principal.Permissions{RowScope: principal.RowScopeAll},
	})
}

// onActivity refolds the pairs one activity touches.
//
// A RELINK is the case that needs the extra id. The payload names the new
// target; the pair the activity used to belong to is not in it, so recomputing
// only what the event names would leave the old colleague still credited with
// a conversation that is no longer theirs. RecomputeEdgesForActivities
// resolves pairs from the activity's own participant rows, which still name
// both sides, so the old pair is refolded too — and, having lost its
// evidence, deleted.
func (g *GraphEdgeGen) onActivity(ctx context.Context, env events.Envelope, activityID ids.UUID) error {
	switch env.Type {
	// The catalog's activity types, in full. Naming one that does not exist
	// is a branch that never runs and a projection that silently never
	// updates — which is exactly how the erasure hole above survived review.
	case "activity.captured", "activity.updated", "activity.archived":
	default:
		return nil
	}
	return database.WithWorkspaceTx(ctx, g.store.pool, func(tx pgx.Tx) error {
		if err := RecomputeEdgesForActivities(ctx, tx, []ids.UUID{activityID}); err != nil {
			return fmt.Errorf("graph-edge: %s: %w", env.Type, err)
		}
		return nil
	})
}

// onPerson refolds or drops the edges to one contact.
func (g *GraphEdgeGen) onPerson(ctx context.Context, env events.Envelope, personID ids.UUID) error {
	return database.WithWorkspaceTx(ctx, g.store.pool, func(tx pgx.Tx) error {
		switch env.Type {
		case "person.merged":
			// The source's edges belong to the survivor now. Dropping the
			// source and refolding it is enough: the merge already repointed
			// the activity links, so refolding the SOURCE id finds nothing and
			// the survivor is refolded when its own event arrives. Both are
			// refolded here rather than relying on that ordering, because a
			// projection that is only correct if two events arrive in order is
			// not correct on an at-least-once bus.
			if err := DropEdgesForPerson(ctx, tx, personID); err != nil {
				return err
			}
			if target := mergeTarget(env); target != ids.Nil {
				return RecomputeEdgesForPerson(ctx, tx, target)
			}
			return nil
		case "person.archived", "person.restored", "person.updated", "person.created":
			return RecomputeEdgesForPerson(ctx, tx, personID)
		default:
			return nil
		}
	})
}

// mergeTarget reads the surviving person from a merge envelope. An absent or
// unparseable target answers Nil, and the caller treats that as "nothing more
// to refold": the survivor's own event will still arrive, and the nightly
// rebuild is the backstop. Guessing an id here would be worse than waiting.
func mergeTarget(env events.Envelope) ids.UUID {
	var payload struct {
		MergedIntoID string `json:"merged_into_id"`
	}
	if len(env.Payload) == 0 {
		return ids.Nil
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return ids.Nil
	}
	id, err := ids.Parse(payload.MergedIntoID)
	if err != nil {
		return ids.Nil
	}
	return id
}
