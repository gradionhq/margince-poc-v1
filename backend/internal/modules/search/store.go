package search

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Hit is one ranked result. Score is ts_rank_cd over the entity's
// search_tsv — comparable across types because every column uses the
// same 'simple' configuration.
type Hit struct {
	Type    string
	ID      ids.UUID
	Title   string
	Snippet string
	Score   float64
}

type Page struct {
	Hits       []Hit
	NextCursor string
	HasMore    bool
}

type Input struct {
	Query  string
	Types  []string
	Limit  int
	Cursor string
}

const defaultLimit = 20

// searchBranches declares one UNION branch per searchable entity: the
// scoped tables, their display title, and whether the caller's
// row-scope rides the owner predicate or the activity link walk. A new
// searchable entity is one row here — the query builder derives the
// rest.
type searchBranch struct {
	entity       string
	table        string
	title        string
	snippet      string
	activityWalk bool
}

var searchBranches = []searchBranch{
	{entity: "person", table: "person", title: "full_name", snippet: "NULL"},
	{entity: "organization", table: "organization", title: "display_name", snippet: "NULL"},
	{entity: "deal", table: "deal", title: "name", snippet: "NULL"},
	{entity: "lead", table: "lead", title: "coalesce(full_name, company_name, email)", snippet: "NULL"},
	{entity: "activity", table: "activity", title: "coalesce(subject, kind)", snippet: "left(coalesce(body, ''), 200)", activityWalk: true},
}

// Search runs the ranked cross-object query (contract /search). Every
// branch carries archived_at IS NULL and the caller's row scope; ranked
// keyset pagination orders (score DESC, type, id) so the cursor is
// stable under concurrent writes.
func (s *Store) Search(ctx context.Context, in Input) (Page, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return Page{}, &BadQueryError{Reason: "q is required"}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	types := in.Types
	if len(types) == 0 {
		for _, b := range searchBranches {
			types = append(types, b.entity)
		}
	}
	for _, t := range types {
		if !knownEntity(t) {
			return Page{}, &BadQueryError{Reason: fmt.Sprintf("unknown type %q", t)}
		}
	}

	var cursor *rankedCursor
	if in.Cursor != "" {
		decoded, err := decodeCursor(in.Cursor)
		if err != nil {
			return Page{}, err
		}
		cursor = &decoded
	}

	var page Page
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var args []any
		arg := func(v any) int { args = append(args, v); return len(args) }
		qPos := arg(query)

		var branches []string
		for _, branch := range searchBranches {
			if !contains(types, branch.entity) {
				continue
			}
			// A hit is a read twice over: object RBAC first (a role
			// without person.read gets no person hits — search must not
			// out-see the entity lists), then the row scope below.
			if err := auth.Require(ctx, branch.entity, principal.ActionRead); err != nil {
				continue
			}
			var scope string
			var err error
			if branch.activityWalk {
				scope, err = auth.ActivityScopeClause(ctx, "t", arg)
			} else {
				scope, err = auth.ScopeClauseFor(ctx, "t", arg)
			}
			if err != nil {
				return err
			}
			sql := fmt.Sprintf(
				`SELECT '%s'::text AS rtype, t.id, %s AS title, %s AS snippet,
				        ts_rank_cd(t.search_tsv, websearch_to_tsquery('simple', $%d))::float8 AS score
				 FROM %s t
				 WHERE t.search_tsv @@ websearch_to_tsquery('simple', $%d)
				   AND t.archived_at IS NULL`,
				branch.entity, branch.title, branch.snippet, qPos, branch.table, qPos)
			if scope != "" {
				sql += " AND " + scope
			}
			branches = append(branches, sql)
		}
		if len(branches) == 0 {
			// Every requested type was denied by object RBAC: an empty
			// page, not an error — search discloses nothing the entity
			// lists would not.
			return nil
		}

		sql := "SELECT rtype, id, title, snippet, score FROM (" + strings.Join(branches, " UNION ALL ") + ") ranked"
		if cursor != nil {
			// Keyset over the ranked order: strictly worse score, or the
			// same score past the (type, id) tie-break.
			sql += fmt.Sprintf(
				` WHERE score < $%d OR (score = $%d AND (rtype, id) > ($%d, $%d))`,
				arg(cursor.Score), len(args), arg(cursor.Type), arg(cursor.ID))
		}
		sql += fmt.Sprintf(" ORDER BY score DESC, rtype, id LIMIT $%d", arg(limit+1))

		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("search: query: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var h Hit
			var title, snippet *string
			if err := rows.Scan(&h.Type, &h.ID, &title, &snippet, &h.Score); err != nil {
				return err
			}
			if title != nil {
				h.Title = *title
			}
			if snippet != nil {
				h.Snippet = strings.TrimSpace(*snippet)
			}
			page.Hits = append(page.Hits, h)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Hits) > limit {
			page.Hits = page.Hits[:limit]
			page.HasMore = true
			last := page.Hits[limit-1]
			page.NextCursor = encodeCursor(rankedCursor{Score: last.Score, Type: last.Type, ID: last.ID})
		}
		return nil
	})
	if err != nil {
		return Page{}, err
	}
	return page, nil
}

// BadQueryError maps to a 422 at the transport.
type BadQueryError struct{ Reason string }

func (e *BadQueryError) Error() string { return "search: " + e.Reason }

// rankedCursor is the (score, type, id) keyset position. Encoding keeps
// full float64 precision (strconv 'g' -1) — a rounded score would skip
// or repeat rows on the boundary.
type rankedCursor struct {
	Score float64
	Type  string
	ID    ids.UUID
}

func encodeCursor(c rankedCursor) string {
	raw := strconv.FormatFloat(c.Score, 'g', -1, 64) + "|" + c.Type + "|" + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (rankedCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return rankedCursor{}, &BadQueryError{Reason: "malformed cursor"}
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return rankedCursor{}, &BadQueryError{Reason: "malformed cursor"}
	}
	score, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return rankedCursor{}, &BadQueryError{Reason: "malformed cursor"}
	}
	id, err := ids.Parse(parts[2])
	if err != nil {
		return rankedCursor{}, &BadQueryError{Reason: "malformed cursor"}
	}
	return rankedCursor{Score: score, Type: parts[1], ID: id}, nil
}

func knownEntity(t string) bool {
	for _, b := range searchBranches {
		if b.entity == t {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
