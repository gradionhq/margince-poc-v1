// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

// The subject-key lookup behind the capture ladder's third rung: which LIVE
// project one of a subject's tokens is the key of. It lives here because
// `project` is this module's table; the caller that needs it reaches it through
// a seam compose injects, never by importing this package.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// MatchProjectKey answers which project a captured subject's tokens name, or
// the zero id when they name none — which is the ordinary answer, since most
// mail carries no project key at all.
//
// Two DISTINCT projects matched means the subject is ambiguous and the answer
// is no project: the ladder has no human in it, so a coin flip here would file
// a message under the wrong body of work with nothing to catch it. Several
// tokens matching the SAME project is not ambiguity — a subject may repeat its
// key in the prefix and the prose.
//
// Archived projects are excluded because a key is unique among LIVE rows only
// (uq_project_key): once a project is archived its key can be reused, so
// matching an archived row could file today's mail under last year's work.
func (s *Store) MatchProjectKey(ctx context.Context, tokens []string) (ids.UUID, error) {
	if len(tokens) == 0 {
		return ids.Nil, nil
	}
	if err := auth.Require(ctx, projectObject, principal.ActionRead); err != nil {
		return ids.Nil, err
	}
	args := []any{tokens}
	arg := func(v any) int { args = append(args, v); return len(args) }
	// This returns a record id, so it carries the row scope like any other read
	// — a project the caller could not open must not be one their mail is filed
	// under. Ambiguity is counted AFTER the scope narrows the set, which is the
	// honest reading: a second match the caller cannot see is not a second
	// answer to their question.
	scope, err := auth.ScopeClauseFor(ctx, projectObject, "", arg)
	if err != nil {
		return ids.Nil, err
	}
	where := `lower(key) = ANY($1) AND key IS NOT NULL AND archived_at IS NULL`
	if scope != "" {
		where += " AND " + scope
	}
	var matched []ids.UUID
	err = s.tx(ctx, func(tx pgx.Tx) error {
		// LIMIT 2 because the query answers a yes/no/ambiguous question, not a
		// list: one row is the match, two is enough to know it is ambiguous,
		// and a third would only be read to be discarded.
		rows, err := tx.Query(ctx, `SELECT id FROM project WHERE `+where+` LIMIT 2`, args...)
		if err != nil {
			return fmt.Errorf("deals: matching a subject's project keys: %w", err)
		}
		matched, err = pgx.CollectRows(rows, pgx.RowTo[ids.UUID])
		return err
	})
	if err != nil {
		return ids.Nil, err
	}
	if len(matched) != 1 {
		return ids.Nil, nil
	}
	return matched[0], nil
}
