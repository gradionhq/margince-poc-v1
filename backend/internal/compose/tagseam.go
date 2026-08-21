// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The tag seam: the agents module asks, collections answers. Declared in
// agents and implemented here, like every cross-module edge (ADR-0054).

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/collections"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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
	if found, ok, err := a.store.FindTag(ctx, name); err != nil || ok {
		return found, err
	}
	created, err := a.store.NewTag(ctx, name, "")
	if err == nil {
		return created.TagID, nil
	}
	// Two callers can both miss the lookup and both try to create; uq_tag_name
	// lets exactly one win. The loser reuses the winner's tag rather than
	// failing a call that asked for a state now true — the reuse this method
	// exists for, arriving a moment later than expected.
	//
	// A name whose only holder is ARCHIVED lands here too and stays a
	// conflict: the index does not exempt archived rows, and quietly
	// resurrecting a word somebody retired is not this call's decision to make.
	if !errors.Is(err, apperrors.ErrConflict) {
		return ids.UUID{}, err
	}
	found, ok, findErr := a.store.FindTag(ctx, name)
	if findErr != nil {
		return ids.UUID{}, findErr
	}
	if !ok {
		return ids.UUID{}, fmt.Errorf("a tag named %q exists but is archived; restore it or use another name: %w",
			name, err)
	}
	return found, nil
}

// ListTags hands the collections module's own cross-module shape straight
// through: TagVocabulary was written for a caller outside that module and
// this is the first one, so there is nothing here to translate beyond the
// field names the wire uses.
func (a tagAdapter) ListTags(ctx context.Context, includeArchived bool) ([]agents.Tag, error) {
	rows, err := a.store.TagVocabulary(ctx, includeArchived)
	if err != nil {
		return nil, err
	}
	out := make([]agents.Tag, 0, len(rows))
	for _, r := range rows {
		out = append(out, agents.Tag{TagID: r.TagID, Name: r.Name, Color: r.Color, Archived: r.Archived})
	}
	return out, nil
}

func (a tagAdapter) EnsureTaggable(ctx context.Context, entityType string, entityID ids.UUID) error {
	return a.store.EnsureTaggable(ctx, entityType, entityID)
}

func (a tagAdapter) FindTag(ctx context.Context, name string) (ids.UUID, bool, error) {
	return a.store.FindTag(ctx, name)
}

func (a tagAdapter) ApplyTag(ctx context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error {
	_, err := a.store.ApplyTag(ctx, ids.From[ids.TagKind](tagID), entityType, entityID)
	return err
}

func (a tagAdapter) RemoveTag(ctx context.Context, tagID ids.UUID, entityType string, entityID ids.UUID) error {
	return a.store.RemoveTag(ctx, ids.From[ids.TagKind](tagID), entityType, entityID)
}
