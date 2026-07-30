// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The flip's migration.Source over the frozen mirror (OVA-WIRE-8 "runs
// the importer against the mirror"): a thin adapter — every mirror
// semantic (ordering, visibility posture, gating) stays in the overlay
// module's Flip* reads; this file only translates shapes.

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// The estate's object classes, named once so the source, the writers,
// and the stage catalog cannot drift on a string literal.
const (
	flipObjectOrganization = "organization"
	flipObjectPerson       = "person"
	flipObjectLead         = "lead"
	flipObjectDeal         = "deal"
	flipObjectActivity     = "activity"
)

// flipImportOrder is the canonical import order: parents before
// dependents (organizations before the persons and deals that reference
// them; activities last so every link target already exists).
var flipImportOrder = []string{
	flipObjectOrganization, flipObjectPerson, flipObjectLead, flipObjectDeal, flipObjectActivity,
}

type mirrorFlipSource struct {
	ms *overlay.MirrorStore
}

var _ migration.Source = mirrorFlipSource{}

func (s mirrorFlipSource) Objects() []string { return flipImportOrder }

func (s mirrorFlipSource) Counts(ctx context.Context) (map[string]int, error) {
	return s.ms.FlipCounts(ctx)
}

func (s mirrorFlipSource) Rows(ctx context.Context, object string, offset, limit int) ([]migration.Row, error) {
	rows, err := s.ms.FlipRows(ctx, object, offset, limit)
	if err != nil {
		return nil, err
	}
	out := make([]migration.Row, 0, len(rows))
	for _, r := range rows {
		fields := r.Fields
		if r.OwnerExternalID != "" {
			// The mirror keeps the incumbent owner id in its own column;
			// the writer resolves it through mirror_user_map, so carry it
			// in-band where the row's other values already live.
			fields = cloneFieldsWith(fields, flipFieldOwnerExternalID, r.OwnerExternalID)
		}
		out = append(out, migration.Row{
			ExternalID:   r.ExternalID,
			Fields:       fields,
			LastSyncedAt: r.LastSyncedAt,
		})
	}
	return out, nil
}

func (s mirrorFlipSource) Associations(ctx context.Context) ([]migration.Assoc, error) {
	edges, err := s.ms.FlipAssociations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]migration.Assoc, 0, len(edges))
	for _, e := range edges {
		out = append(out, migration.Assoc{
			FromType: e.FromType, FromID: e.FromID,
			ToType: e.ToType, ToID: e.ToID,
			Category: e.Category, Label: e.Label,
		})
	}
	return out, nil
}

// flipFieldOwnerExternalID carries the mirror row's incumbent owner id
// into the writer's field map without colliding with a mapped column
// (mapping keys are native column names; the underscore prefix is not).
const flipFieldOwnerExternalID = "_owner_external_id"

func cloneFieldsWith(fields map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		out[k] = v
	}
	out[key] = value
	return out
}
