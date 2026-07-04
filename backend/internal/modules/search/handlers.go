package search

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// Handlers is the module's transport slice; compose embeds it so the
// generated Search stub is shadowed by real code.
type Handlers struct {
	store *Store
}

func NewHandlers(pool *pgxpool.Pool) Handlers {
	return Handlers{store: NewStore(pool)}
}

func (h Handlers) Search(w http.ResponseWriter, r *http.Request, params crmcontracts.SearchParams) {
	in := Input{Query: params.Q}
	if params.Types != nil {
		for _, t := range *params.Types {
			in.Types = append(in.Types, string(t))
		}
	}
	if params.Cursor != nil {
		in.Cursor = *params.Cursor
	}
	if params.Limit != nil {
		in.Limit = *params.Limit
	}

	page, err := h.store.Search(r.Context(), in)
	if err != nil {
		var bad *BadQueryError
		if errors.As(err, &bad) {
			httperr.Write(w, r, httperr.Validation("q", "invalid_query", bad.Reason))
			return
		}
		httperr.Write(w, r, err)
		return
	}

	data := make([]crmcontracts.SearchResult, 0, len(page.Hits))
	for _, hit := range page.Hits {
		result := crmcontracts.SearchResult{
			Id:    openapi_types.UUID(hit.ID),
			Type:  crmcontracts.SearchResultType(hit.Type),
			Score: ptr(float32(hit.Score)),
		}
		if hit.Title != "" {
			result.Title = ptr(hit.Title)
		}
		if hit.Snippet != "" {
			result.Snippet = ptr(hit.Snippet)
		}
		data = append(data, result)
	}
	pageInfo := crmcontracts.PageInfo{HasMore: page.HasMore}
	if page.NextCursor != "" {
		pageInfo.NextCursor = &page.NextCursor
	}
	httperr.WriteJSON(w, http.StatusOK, crmcontracts.SearchResponse{Data: data, Page: pageInfo})
}

func ptr[T any](v T) *T { return &v }
