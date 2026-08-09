// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// Retriever implements the shared/ports/retrieval seam over this
// module's hybrid search and the context graph — the ONE place ranking
// and context assembly live, so the AI layers and the intent tools stop
// re-stitching rows per caller.
type Retriever struct {
	store    *Store
	embedder Embedder
}

func NewRetriever(store *Store, embedder Embedder) *Retriever {
	return &Retriever{store: store, embedder: embedder}
}

var _ retrieval.Retriever = (*Retriever)(nil)

// Search narrows BOTH hybrid lanes to the record types asked for rather than
// filtering the fused page afterwards. The page is a global top-N, so
// post-filtering spends it on types the caller never named and can answer
// nothing for a type that merely ranks below five others — a recall hole a
// caller reads as "there are no such records".
func (r *Retriever) Search(ctx context.Context, q retrieval.Query) (retrieval.Result, error) {
	types := make([]string, 0, len(q.EntityTypes))
	for _, t := range q.EntityTypes {
		types = append(types, string(t))
	}
	hits, semantic, err := r.store.HybridSearch(ctx, q.Text, r.embedder, clampLimit(q.Limit), types...)
	if err != nil {
		return retrieval.Result{}, err
	}
	out := retrieval.Result{Hits: make([]retrieval.Hit, 0, len(hits)), SemanticRanking: semantic}
	for _, hit := range hits {
		out.Hits = append(out.Hits, retrieval.Hit{
			Ref:   datasource.EntityRef{Type: datasource.EntityType(hit.Type), ID: hit.ID},
			Score: hit.Score,
			Evidence: []retrieval.Evidence{{
				Source:  hit.Type + ":" + hit.ID.String(),
				Snippet: firstNonEmpty(hit.Snippet, hit.Title),
			}},
		})
	}
	return out, nil
}

// AssembleContext is the §2.2 assembled-picture affordance for one
// anchor: profile, recent touches, related people/organizations, and
// open tasks — every item provenance-stamped, every read row-scoped.
func (r *Retriever) AssembleContext(ctx context.Context, anchor datasource.EntityRef, opts retrieval.AssembleOptions) (retrieval.Context, error) {
	maxItems := opts.MaxItems
	if maxItems <= 0 {
		maxItems = 5
	}
	assembled, err := r.store.assembleGraph(ctx, string(anchor.Type), anchor.ID, maxItems)
	if err != nil {
		return retrieval.Context{}, fmt.Errorf("search: assemble context: %w", err)
	}
	out := retrieval.Context{Anchor: anchor}
	for _, section := range assembled {
		sec := retrieval.Section{Name: section.name}
		for _, item := range section.items {
			sec.Items = append(sec.Items, retrieval.Item{
				Ref:     datasource.EntityRef{Type: datasource.EntityType(item.entityType), ID: item.id},
				Summary: item.summary,
				Evidence: []retrieval.Evidence{{
					Source:  item.entityType + ":" + item.id.String(),
					Snippet: item.summary,
				}},
			})
		}
		if len(sec.Items) > 0 {
			out.Sections = append(out.Sections, sec)
		}
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
