// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cross-module edge behind query_workspace: the search module compiles,
// validates and executes a plan; the agents module owns the tool and turns the
// refs it answers into records. Neither imports the other (ADR-0054 §3), so the
// whole path is composed here, once.
//
// ONE VOCABULARY, TWO CONSUMERS. The resolver built here is the same one
// mcpedge.go publishes as margince://schema/query. That is not tidiness: a
// document advertising a field the executor resolves differently would refuse
// at execution what it advertised at discovery, which is the failure the
// storage-backed vocabulary was introduced to close. Building it in one
// function is what makes the two provably the same.

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/modules/search"
)

// queryVocabulary is the resolver both the published schema and the executor
// read: the contract's fields, narrowed to what this workspace's own tables can
// answer, widened by the custom fields it has declared.
func queryVocabulary(pool *pgxpool.Pool) *search.VocabularyResolver {
	return search.NewVocabularyResolver().
		WithFieldCatalog(customfields.NewService(pool, nil)).
		WithColumnReader(search.NewColumnCatalog(InstallationDB(pool)))
}

// queryRunner joins the three steps a plan takes into the one function the tool
// seam calls.
//
// The embedder is the role's own retrieval embed lane, and it is the SAME one
// the intent retriever takes — one binding for both, decided at the
// composition root. A role that resolved no model path passes nil, and a
// `similar_to` clause then ranks lexically and the executor says so: the answer
// comes back partial_degraded carrying semantic_ranking_degraded_to_lexical.
// That marker is not a fallback for the unbound case alone — an embed call that
// FAILS answers it too, because a caller reading a ranked page needs to know
// which lane ranked it, not why.
func queryRunner(pool *pgxpool.Pool, embedder search.Embedder) agents.QueryRunner {
	validator := search.NewPlanValidator(queryVocabulary(pool))
	executor := search.NewQueryExecutor(search.NewStore(InstallationDB(pool)), embedder,
		search.NewColumnCatalog(InstallationDB(pool))).
		// The place cache a radius predicate resolves its centre against.
		// Injected here rather than imported, like every other cross-module
		// edge (ADR-0054): search owns the port, people owns the table.
		WithPlaces(placeCache{people: people.NewStore(InstallationDB(pool))})
	return func(ctx context.Context, raw json.RawMessage) (agents.QueryAnswer, error) {
		plan, err := search.DecodePlan(raw)
		if err != nil {
			return agents.QueryAnswer{}, err
		}
		validated, err := validator.Validate(ctx, plan)
		if err != nil {
			return agents.QueryAnswer{}, err
		}
		result, err := executor.Execute(ctx, validated)
		if err != nil {
			return agents.QueryAnswer{}, err
		}
		return queryAnswerOf(result), nil
	}
}

// queryAnswerOf carries the executor's result across the seam. The two types
// are deliberately separate: the tool's is the WIRE and the executor's is the
// module's own, and collapsing them would make a rename in either one a silent
// change to the other.
func queryAnswerOf(result search.QueryResult) agents.QueryAnswer {
	answer := agents.QueryAnswer{
		Refs:      make([]agents.QueryRef, 0, len(result.Rows)),
		Coverage:  result.Coverage,
		Notes:     make([]agents.QueryNote, 0, len(result.Notes)),
		Narrative: result.Narrative,
		Limit:     result.Limit,
	}
	for _, row := range result.Rows {
		ref := agents.QueryRef{
			Type:       row.Type,
			ID:         row.ID,
			Score:      row.Score,
			DistanceKM: row.DistanceKM,
			Evidence:   make([]agents.QueryEvidence, 0, len(row.Evidence)),
		}
		for _, evidence := range row.Evidence {
			ref.Evidence = append(ref.Evidence, agents.QueryEvidence{
				Relation:   evidence.Relation,
				RecordType: evidence.Type,
				ID:         evidence.ID,
				Title:      evidence.Title,
			})
		}
		answer.Refs = append(answer.Refs, ref)
	}
	for _, note := range result.Notes {
		answer.Notes = append(answer.Notes, agents.QueryNote{
			Code: note.Code, Path: note.Path, Detail: note.Detail,
		})
	}
	return answer
}

// placeCache adapts the people store's place cache to search's PlaceResolver.
//
// LOOKUP ONLY, and the port has no other method by design: query_workspace is
// declared workspace-local, Scope.Egresses() is derived from that declaration,
// and a resolver able to reach a geocoder would make the declaration
// unenforceable by construction rather than by discipline. A place this
// INSTALLATION has never resolved answers an honest note; the enrich-scoped
// door is where a caller goes to have one looked up.
//
// The cache is installation-wide, not per workspace — see PlaceResolver for
// why that is safe here and what would have to change if it stopped being.
type placeCache struct{ people *people.Store }

func (p placeCache) LookupPlace(ctx context.Context, query string) (search.Point, bool, error) {
	found, ok, err := p.people.LookupPlace(ctx, query)
	if err != nil || !ok {
		return search.Point{}, false, err
	}
	return search.Point{Lat: found.Lat, Lon: found.Lon}, true, nil
}
