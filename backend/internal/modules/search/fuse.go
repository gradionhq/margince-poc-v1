// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"fmt"
	"log/slog"
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
//
// types narrows BOTH lanes to the record types asked for, and narrowing
// them is not the same as filtering the fused page afterwards: the page
// is a global top-N, so post-filtering spends it on types the caller
// never asked about and can answer nothing for a record type that is
// simply less similar overall than five others. Empty means every type.
//
// The BOOL says whether the vector lane actually contributed. Every caller
// that publishes its ranking as semantic owes the answer that word, and there
// are two ways to lose the lane — see degradeToLexical.
func (s *Store) HybridSearch(ctx context.Context, query string, embedder Embedder, limit int, types ...string) ([]Hit, bool, error) {
	limit = clampLimit(limit)
	// Overfetch both lanes: an entity ranked just past `limit` in each
	// lane can still fuse into the top set.
	laneDepth := limit * 3

	lexical, err := s.Search(ctx, Input{Query: query, Types: types, Limit: laneDepth})
	if err != nil {
		return nil, false, err
	}
	// A nil embedder (no declared embed lane at all) and a non-nil
	// embedder whose EmbedIdentity() is "" (--ai-fake, or any routing
	// config that never bound an embeddings model) are the SAME shape
	// from the query side: no live embed lane to rank against, so both
	// degrade to the lexical lane alone, honestly, never a nil-pointer or
	// a call into an unbound Embed().
	if !embeddingLaneBound(embedder) {
		return degradeToLexical(lexical.Hits, limit), false, nil
	}
	// identity and dims both ride the SAME binding the query is about
	// to embed under: dims sizes the request, and identity is threaded
	// into SimilarEntities below so the read side only ever ranks rows
	// stored under this exact identity — the filter that keeps a
	// binding swap's stale, differently-sized rows out of this query's
	// results (and out of the <=> operator's reach entirely).
	identity, dims := embedder.EmbedIdentity()

	// A FAILED embed call degrades exactly as an absent lane does, and does not
	// fail the search. The embed lane became a request-path dependency the day
	// it was bound to one, and a provider being unreachable would otherwise
	// turn every ranked question into a 5xx — where the same deployment,
	// yesterday, answered it lexically. A lexical page that SAYS it is lexical
	// is a worse answer than a semantic one and a far better answer than none.
	//
	// This is the one place in this module that swallows a provider fault into
	// a degraded answer, so it is also the one place that has to be loud about
	// it: the caller learns through the returned false, and the operator
	// through the log.
	queryEmb, err := embedder.Embed(ctx, model.EmbedRequest{Inputs: []string{query}, Dimensions: dims})
	if err != nil {
		// A CANCELLED REQUEST IS NOT A DEGRADED ONE. The caller is gone or their
		// deadline has passed, so a lexical page has nobody to reach — and
		// reporting a cancellation as this workspace's embed lane being down
		// would describe the caller's own timeout as a property of the
		// installation.
		if ctx.Err() != nil {
			return nil, false, err
		}
		slog.WarnContext(ctx, "embedding the query failed; ranking this search lexically",
			"identity", identity, "err", err)
		return degradeToLexical(lexical.Hits, limit), false, nil
	}
	if len(queryEmb.Vectors) != 1 {
		// NOT a degradation. A lane that answers a one-input request with
		// anything other than one vector is broken rather than absent, and
		// serving a quietly lexical page over it would hide a defect that
		// every embed on this binding is also hitting.
		return nil, false, fmt.Errorf("search: query embedding returned %d vectors", len(queryEmb.Vectors))
	}
	vector, err := s.SimilarEntities(ctx, queryEmb.Vectors[0], identity, laneDepth, types...)
	if err != nil {
		return nil, false, err
	}
	// SEMANTIC MEANS THE VECTOR LANE CONTRIBUTED, not that it ran. An empty
	// vector page is the ordinary shape after a binding change and before the
	// reindex catches up: the query embeds fine, and SimilarEntities filters to
	// rows stored under this exact identity, of which there are none yet. What
	// comes back is then the lexical page, and calling it semantic would put the
	// most misleading label on an answer at exactly the moment it is least true.
	return fuseRankedResults(lexical.Hits, vector, limit), len(vector) > 0, nil
}

// degradeToLexical is the answer when the vector lane cannot contribute: the
// lexical lane's own hits, cut to the page the caller asked for. The overfetch
// above is there to feed fusion, and handing it back whole would answer a
// degraded query with three times the page a healthy one returns.
func degradeToLexical(hits []Hit, limit int) []Hit {
	if len(hits) > limit {
		return hits[:limit]
	}
	return hits
}

// embeddingLaneBound asks whether there is a live embed lane to rank against.
// A nil embedder and one whose identity is empty (the offline fake, or a
// routing config that never bound an embeddings model) are the same shape from
// the query side.
func embeddingLaneBound(embedder Embedder) bool {
	if embedder == nil {
		return false
	}
	identity, _ := embedder.EmbedIdentity()
	return identity != ""
}

// fuseRankedResults merges the two already-filtered lanes by reciprocal
// rank fusion: each lane contributes 1/(k+rank) to an entity's score, so
// an entity both lanes rank beats either lane's solo favorite. Each returned
// Hit.Score is the FUSED score, not the lane score the hit arrived with.
func fuseRankedResults(lexical []Hit, vector []VectorHit, limit int) []Hit {
	type fused struct {
		hit   Hit
		score float64
	}
	byKey := map[string]*fused{}
	key := func(entityType, id string) string { return entityType + ":" + id }

	for rank, hit := range lexical {
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
	return hits
}
