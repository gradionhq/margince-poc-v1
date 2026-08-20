// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The tag seam: the agents module asks, collections answers. Declared in
// agents and implemented here, like every cross-module edge (ADR-0054).

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

type tagAdapter struct{ store *collections.Store }

// tagSeam binds the tag verbs to the one store the HTTP transport uses, so a
// tag applied over MCP and one applied in the web app pass the same gates and
// write the same audit row.
func tagSeam(pool *pgxpool.Pool) agents.Tags {
	return tagAdapter{store: collections.NewStore(InstallationDB(pool))}
}

func (a tagAdapter) ListTags(ctx context.Context, includeArchived bool) ([]agents.Tag, error) {
	summaries, err := a.store.TagVocabulary(ctx, includeArchived)
	if err != nil {
		return nil, err
	}
	out := make([]agents.Tag, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, agents.Tag{TagID: s.TagID, Name: s.Name, Color: s.Color, Archived: s.Archived})
	}
	return out, nil
}

func (a tagAdapter) CreateTag(ctx context.Context, name, color string) (agents.Tag, error) {
	s, err := a.store.NewTag(ctx, name, color)
	if err != nil {
		return agents.Tag{}, err
	}
	return agents.Tag{TagID: s.TagID, Name: s.Name, Color: s.Color, Archived: s.Archived}, nil
}

func (a tagAdapter) ApplyTag(ctx context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error {
	_, err := a.store.ApplyTag(ctx, ids.From[ids.TagKind](tagID), entityType, entityID)
	return err
}

func (a tagAdapter) RemoveTag(ctx context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error {
	return a.store.RemoveTag(ctx, ids.From[ids.TagKind](tagID), entityType, entityID)
}
