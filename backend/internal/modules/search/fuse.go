// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"fmt"
	"sort"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// rrfK is the reciprocal-rank-fusion constant (the literature default;
// ADR-0022 §6): large enough that a single lane's top rank cannot
// drown out agreement between lanes.
const rrfK = 60

// HybridSearch fuses the lexical and vector lanes with RRF
// (B-EP05.18): each lane contributes 1/(k+rank), so an entity both
// lanes agree on outranks either lane's solo favorite. Both lanes are
// already RBAC- and row-scope-filtered; fusion adds no visibility.
func (s *Store) HybridSearch(ctx context.Context, query string, embedder Embedder, limit int) ([]Hit, error) {
	limit = clampLimit(limit)
	// Overfetch both lanes: an entity ranked just past `limit` in each
	// lane can still fuse into the top set.
	laneDepth := limit * 3

	lexical, err := s.Search(ctx, Input{Query: query, Limit: laneDepth})
	if err != nil {
		return nil, err
	}
	if embedder == nil {
		// A deployment with no declared embed lane still searches — the
		// lexical lane alone, honestly degraded, never a nil-pointer.
		hits := lexical.Hits
		if len(hits) > limit {
			hits = hits[:limit]
		}
		return hits, nil
	}

	// identity and dims both ride the SAME binding the query is about to
	// embed under: dims sizes the request, and identity is threaded into
	// SimilarEntities below so the read side only ever ranks rows stored
	// under this exact identity — the filter that keeps a binding swap's
	// stale, differently-sized rows out of this query's results (and out
	// of the <=> operator's reach entirely).
	identity, dims := embedder.EmbedIdentity()
	queryEmb, err := embedder.Embed(ctx, model.EmbedRequest{Inputs: []string{query}, Dimensions: dims})
	if err != nil {
		return nil, fmt.Errorf("search: embedding the query: %w", err)
	}
	if len(queryEmb.Vectors) != 1 {
		return nil, fmt.Errorf("search: query embedding returned %d vectors", len(queryEmb.Vectors))
	}
	vector, err := s.SimilarEntities(ctx, queryEmb.Vectors[0], identity, laneDepth)
	if err != nil {
		return nil, err
	}

	type fused struct {
		hit   Hit
		score float64
	}
	byKey := map[string]*fused{}
	key := func(entityType, id string) string { return entityType + ":" + id }

	for rank, hit := range lexical.Hits {
		byKey[key(hit.Type, hit.ID.String())] = &fused{hit: hit, score: 1.0 / float64(rrfK+rank+1)}
	}
	for rank, vh := range vector {
		k := key(vh.Type, vh.ID.String())
		contribution := 1.0 / float64(rrfK+rank+1)
		if existing, ok := byKey[k]; ok {
			existing.score += contribution
			continue
		}
		byKey[k] = &fused{
			hit:   Hit{Type: vh.Type, ID: vh.ID, Title: vh.Title, Score: vh.Similarity},
			score: contribution,
		}
	}

	out := make([]fused, 0, len(byKey))
	for _, f := range byKey {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		// Deterministic tie-break: type then id (formulas §10 discipline).
		if out[i].hit.Type != out[j].hit.Type {
			return out[i].hit.Type < out[j].hit.Type
		}
		return out[i].hit.ID.String() < out[j].hit.ID.String()
	})
	if len(out) > limit {
		out = out[:limit]
	}
	hits := make([]Hit, len(out))
	for i, f := range out {
		hits[i] = f.hit
		hits[i].Score = f.score
	}
	return hits, nil
}
