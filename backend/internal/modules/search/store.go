// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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

// branchScope is the ONE admission + row-scope resolution every union
// branch (lexical and vector alike) runs: object RBAC hides a denied
// type silently, then the branch carries the caller's scope clause.
func branchScope(ctx context.Context, branch searchBranch, arg func(any) int) (scope string, admitted bool, err error) {
	if auth.Require(ctx, branch.entity, principal.ActionRead) != nil {
		return "", false, nil
	}
	if branch.activityWalk {
		scope, err = auth.ActivityScopeClause(ctx, "t", arg)
	} else {
		scope, err = auth.ScopeClauseFor(ctx, branch.entity, "t", arg)
	}
	return scope, true, err
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
	limit := clampLimit(in.Limit)
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

		branches, err := admittedBranchSQL(ctx, types, qPos, arg)
		if err != nil {
			return err
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
		page, err = scanRankedPage(rows, limit)
		return err
	})
	if err != nil {
		return Page{}, err
	}
	return page, nil
}

// admittedBranchSQL builds one ranked SELECT per requested-and-admitted
// entity type. A hit is a read twice over: object RBAC first (a role
// without person.read gets no person hits — search must not out-see the
// entity lists), then the row scope.
func admittedBranchSQL(ctx context.Context, types []string, qPos int, arg func(any) int) ([]string, error) {
	var branches []string
	for _, branch := range searchBranches {
		if !slices.Contains(types, branch.entity) {
			continue
		}
		scope, admitted, err := branchScope(ctx, branch, arg)
		if err != nil {
			return nil, err
		}
		if !admitted {
			continue
		}
		// Name entities parse the query 'simple' (unaccented — Muller
		// finds Müller), OR-ed with the apostrophe-collapsed parse so
		// "o'reilly" also reaches a row stored as "OReilly" (the index
		// side carries the collapsed tokens, migration 0077); the
		// activity branch additionally ORs the German/English stemmed
		// parses so "Vertrag" reaches rows whose tsvector stemmed
		// "Verträge" under their captured language.
		tsquery := fmt.Sprintf(
			`(websearch_to_tsquery('simple', f_unaccent($%[1]d)) || websearch_to_tsquery('simple', f_fold_apostrophes($%[1]d)))`,
			qPos)
		if branch.entity == "activity" {
			tsquery = fmt.Sprintf(
				`(websearch_to_tsquery('simple', f_unaccent($%[1]d)) || websearch_to_tsquery('simple', f_fold_apostrophes($%[1]d)) || websearch_to_tsquery('german', f_unaccent($%[1]d)) || websearch_to_tsquery('english', f_unaccent($%[1]d)))`,
				qPos)
		}
		sql := fmt.Sprintf(
			`SELECT '%s'::text AS rtype, t.id, %s AS title, %s AS snippet,
			        ts_rank_cd(t.search_tsv, %s)::float8 AS score
			 FROM %s t
			 WHERE t.search_tsv @@ %s
			   AND t.archived_at IS NULL`,
			branch.entity, branch.title, branch.snippet, tsquery, branch.table, tsquery)
		if scope != "" {
			sql += " AND " + scope
		}
		branches = append(branches, sql)
	}
	return branches, nil
}

// scanRankedPage materializes the ranked rows and derives the keyset
// cursor from the limit+1 overfetch.
func scanRankedPage(rows pgx.Rows, limit int) (Page, error) {
	var page Page
	for rows.Next() {
		var h Hit
		var title, snippet *string
		if err := rows.Scan(&h.Type, &h.ID, &title, &snippet, &h.Score); err != nil {
			return Page{}, err
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
		return Page{}, err
	}
	if len(page.Hits) > limit {
		page.Hits = page.Hits[:limit]
		page.HasMore = true
		last := page.Hits[limit-1]
		page.NextCursor = encodeCursor(rankedCursor{Score: last.Score, Type: last.Type, ID: last.ID})
	}
	return page, nil
}

// BadQueryError maps to a 422 at the transport.
type BadQueryError struct{ Reason string }

// reasonMalformedCursor is the BadQueryError reason for any un-decodable
// keyset cursor — one spelling so the 422 body never drifts between paths.
const reasonMalformedCursor = "malformed cursor"

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
		return rankedCursor{}, &BadQueryError{Reason: reasonMalformedCursor}
	}
	parts := strings.SplitN(string(raw), "|", 3)
	if len(parts) != 3 {
		return rankedCursor{}, &BadQueryError{Reason: reasonMalformedCursor}
	}
	score, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return rankedCursor{}, &BadQueryError{Reason: reasonMalformedCursor}
	}
	id, err := ids.Parse(parts[2])
	if err != nil {
		return rankedCursor{}, &BadQueryError{Reason: reasonMalformedCursor}
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

// clampLimit maps this module's zero-means-unset ints onto the shared
// CAP-PAGE bounds (default 50, max 200).
func clampLimit(v int) int {
	if v <= 0 {
		return storekit.ClampLimit(nil)
	}
	return storekit.ClampLimit(&v)
}
