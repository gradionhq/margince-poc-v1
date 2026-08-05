// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cross-module edges behind the three lifecycle tools. Each adapter calls
// the owning module's OWN entry point — the same one the REST handler calls —
// so the version fence, the RBAC gate and the audit+outbox write shape are
// reached once and not twice. The tool and the route are two transports onto
// one behaviour, which is the whole claim `x-mcp-tool` makes.
//
// The adapters marshal to json.RawMessage here rather than in the agents
// module: the wire shape is the contract's, and this is the layer that owns the
// contract types.

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type activityRelinker struct{ store *activities.Store }

func (a activityRelinker) RelinkActivity(
	ctx context.Context, activityID ids.UUID, entityType string, entityID ids.UUID, replaceExistingOfType bool,
) (json.RawMessage, error) {
	out, err := a.store.RelinkActivity(ctx, ids.From[ids.ActivityKind](activityID), activities.RelinkActivityInput{
		EntityType:            entityType,
		EntityID:              entityID,
		ReplaceExistingOfType: replaceExistingOfType,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

type leadDisqualifier struct{ store *people.Store }

func (l leadDisqualifier) DisqualifyLead(ctx context.Context, id ids.UUID) (json.RawMessage, error) {
	out, err := l.store.DisqualifyLead(ctx, ids.From[ids.LeadKind](id))
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

type projectPhaseAdvancer struct{ store *deals.Store }

func (p projectPhaseAdvancer) AdvanceProjectPhase(
	ctx context.Context, id ids.UUID, toPhase string, reason *string, ifVersion *int64,
) (json.RawMessage, error) {
	out, err := p.store.AdvanceProjectPhase(ctx, ids.From[ids.ProjectKind](id), deals.AdvanceProjectPhaseInput{
		ToPhase:   toPhase,
		Reason:    reason,
		IfVersion: ifVersion,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// lifecycleSeams builds the three adapters over one pool.
func lifecycleSeams(pool *pgxpool.Pool) (activityRelinker, leadDisqualifier, projectPhaseAdvancer) {
	return activityRelinker{store: activities.NewStore(pool)},
		leadDisqualifier{store: people.NewStore(pool)},
		projectPhaseAdvancer{store: deals.NewStore(pool)}
}
