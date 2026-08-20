// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The tag seam: the agents module asks, collections answers. Declared in
// agents and implemented here, like every cross-module edge (ADR-0054).

import (
	"context"
	"strings"

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

// EnsureTag reuses the workspace's own word before minting a new one.
//
// Case-insensitive, because "K5 Conference" and "k5 conference" are the same
// tag to everyone except a database, and a vocabulary that holds both has
// stopped being a vocabulary. An ARCHIVED tag of that name is not reused: it
// was retired on purpose, and quietly resurrecting it would undo that decision
// on the strength of a coincidence of spelling.
func (a tagAdapter) EnsureTag(ctx context.Context, name string) (ids.UUID, error) {
	existing, err := a.store.TagVocabulary(ctx, false)
	if err != nil {
		return ids.UUID{}, err
	}
	for _, t := range existing {
		if strings.EqualFold(strings.TrimSpace(t.Name), strings.TrimSpace(name)) {
			return t.TagID, nil
		}
	}
	created, err := a.store.NewTag(ctx, name, "")
	if err != nil {
		return ids.UUID{}, err
	}
	return created.TagID, nil
}

func (a tagAdapter) ApplyTag(ctx context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error {
	_, err := a.store.ApplyTag(ctx, ids.From[ids.TagKind](tagID), entityType, entityID)
	return err
}

func (a tagAdapter) RemoveTag(ctx context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error {
	return a.store.RemoveTag(ctx, ids.From[ids.TagKind](tagID), entityType, entityID)
}
