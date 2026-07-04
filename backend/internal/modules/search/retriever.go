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

func (r *Retriever) Search(ctx context.Context, q retrieval.Query) ([]retrieval.Hit, error) {
	limit := clampLimit(q.Limit)
	hits, err := r.store.HybridSearch(ctx, q.Text, r.embedder, limit)
	if err != nil {
		return nil, err
	}
	wanted := map[datasource.EntityType]bool{}
	for _, t := range q.EntityTypes {
		wanted[t] = true
	}
	out := make([]retrieval.Hit, 0, len(hits))
	for _, hit := range hits {
		entityType := datasource.EntityType(hit.Type)
		if len(wanted) > 0 && !wanted[entityType] {
			continue
		}
		out = append(out, retrieval.Hit{
			Ref:   datasource.EntityRef{Type: entityType, ID: hit.ID},
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
