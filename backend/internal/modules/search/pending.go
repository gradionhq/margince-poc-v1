// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

// What is still unembedded, and how much text that is.
//
// The declaration of WHICH tables carry embeddable text sits here with the
// queries that count them, because the two only make sense together: adding a
// searchable entity means adding a row to pendingSources AND to embedgen.go's
// embedText, and the pending count and the live indexer must agree on what
// "this entity's text" means.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// pendingSource mirrors one embedText entry (embedgen.go:35-41) rewritten
// from a per-id lookup into a set-form expression: the same source
// columns, aliased to t, so the two never drift into indexing different
// text.
type pendingSource struct {
	table string
	text  string // expression over the aliased table t
	// tenantScoped says whether this table still carries workspace_id.
	// ADR-0091 §8 phase D removes it table by table, and this query is ONE
	// statement templated over all six — so the predicate has to be per-source
	// while they disagree, or it fails outright for whichever has already lost
	// the column. Every entry goes false as its slice lands, and the field goes
	// with the last one.
	tenantScoped bool
}

// pendingSources is the set-form counterpart to embedgen.go's embedText —
// one entry per embeddable entity, in the exact source-column shape that
// module maintains per-row. Adding a searchable entity means adding a row
// to BOTH maps; they must never diverge, since the pending count and the
// live indexer must agree on what "this entity's text" means.
var pendingSources = map[string]pendingSource{
	entityPerson:       {table: entityPerson, text: "t.full_name", tenantScoped: true},
	entityOrganization: {table: entityOrganization, text: "concat_ws(' ', t.display_name, t.legal_name, t.industry)", tenantScoped: true},
	entityDeal:         {table: entityDeal, text: "t.name"},
	entityLead:         {table: entityLead, text: "concat_ws(' ', t.full_name, t.company_name, t.title)", tenantScoped: true},
	entityActivity:     {table: entityActivity, text: "concat_ws(' ', t.subject, t.body)", tenantScoped: true},
	entityProject:      {table: entityProject, text: "concat_ws(' ', t.name, t.key, t.description)"},
}

// PendingByWorkspace is the per-workspace count of live, non-empty-text
// embeddable entities that lack a current-identity embedding row — the
// same set EntitiesPending totals and TokenSumByWorkspace prices. system-
// principal enumeration (mirrors embedgen.go:51-56): this is an index-
// maintenance rollup, not a user-facing read, so it must see every live
// entity regardless of any one caller's row scope.
func (s *Store) PendingByWorkspace(ctx context.Context, currentIdentity string) (map[ids.WorkspaceID]int, error) {
	counts, _, err := s.pendingStats(ctx, currentIdentity)
	return counts, err
}

// TokenSumByWorkspace is the per-workspace SUM(octet_length(<embedText
// source>))/4 over the same pending set PendingByWorkspace counts — a
// rough 4-UTF-8-bytes-per-token estimate (the same convention as
// ai/router.go:410 and ai/fake.go:113, which count bytes not runes, so a
// non-ASCII corpus is not undercounted), feeding Task 14's advisory cost
// preview. No corpus materialization and no model call: the length lives
// in the source columns already.
func (s *Store) TokenSumByWorkspace(ctx context.Context, currentIdentity string) (map[ids.WorkspaceID]int64, error) {
	_, tokens, err := s.pendingStats(ctx, currentIdentity)
	return tokens, err
}

// EntitiesPending is the fleet-wide total — the sum of PendingByWorkspace,
// and the second operand of ReindexNeeded's OR.
func (s *Store) EntitiesPending(ctx context.Context, currentIdentity string) (int, error) {
	counts, err := s.PendingByWorkspace(ctx, currentIdentity)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, c := range counts {
		total += c
	}
	return total, nil
}

// pendingStats enumerates the fleet and, per workspace, counts and sums
// (as the system principal) every embeddable entity whose source text is
// non-empty and which carries no embedding row at currentIdentity. The
// non-empty qualifier is required: an empty-text entity never gets an
// embedding row at all (embedding.go:47-48, UpsertEmbedding's early
// return), so without it such a row would count as pending forever —
// counting the row's ABSENCE, rather than requiring a stale one, is what
// also covers a wiped store (migration 0114's TRUNCATE) as a rebuild path.
func (s *Store) pendingStats(ctx context.Context, currentIdentity string) (map[ids.WorkspaceID]int, map[ids.WorkspaceID]int64, error) {
	workspaces, err := s.fleetWorkspaceIDs(ctx)
	if err != nil {
		return nil, nil, err
	}

	counts := make(map[ids.WorkspaceID]int, len(workspaces))
	tokens := make(map[ids.WorkspaceID]int64, len(workspaces))
	for _, wsID := range workspaces {
		// The generator reads AS the system, same posture as EmbedGen
		// (embedgen.go:51-56): a rollup built through one caller's row
		// scope would silently under-report entities the caller cannot see.
		wsCtx := systemWorkspaceContext(ctx, wsID.UUID)

		// A store bound to THIS tenant: the workspace a read is scoped to is
		// the handle's, so counting every workspace through the enumerating
		// store would report the same tenant's total under every id.
		count, length, err := s.forWorkspace(wsID).workspacePending(wsCtx, currentIdentity)
		if err != nil {
			return nil, nil, err
		}
		counts[wsID] = count
		tokens[wsID] = length / 4
	}
	return counts, tokens, nil
}

// workspacePending runs one SET-form query per embeddable entity type,
// summing counts and text lengths across all of them for the workspace
// bound in ctx.
func (s *Store) workspacePending(ctx context.Context, currentIdentity string) (count int, length int64, err error) {
	txErr := s.db.Tx(ctx, func(tx pgx.Tx) error {
		for entityType, src := range pendingSources {
			// The workspace predicates are the query's own — tenant isolation
			// used to supply them, so this counted one tenant's backlog without
			// saying so, and an unscoped count reports the whole installation's
			// under every workspace the caller enumerates. They are conditional
			// only because phase D has reached some of these tables and not
			// others; see pendingSource.tenantScoped.
			// Two fragments, not one: the entity predicate belongs in the OUTER
			// where, and the embedding leg inside the NOT EXISTS. Folding the
			// entity predicate into the subquery would invert it — a row of
			// another workspace would match nothing there, so NOT EXISTS would
			// be true and the row would be counted rather than excluded.
			entityScope, embeddingScope := "", ""
			if src.tenantScoped {
				entityScope = `
				  AND t.workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`
				embeddingScope = ` AND e.workspace_id = t.workspace_id`
			}
			sql := fmt.Sprintf(`
				SELECT count(*), coalesce(sum(octet_length(btrim(%s))), 0)
				FROM %s t
				WHERE t.archived_at IS NULL
				  AND btrim(%s) <> ''%s
				  AND NOT EXISTS (
				        SELECT 1 FROM embedding e
				        WHERE e.entity_type = '%s' AND e.entity_id = t.id AND e.model = $1%s)`,
				src.text, src.table, src.text, entityScope, entityType, embeddingScope)
			var c int
			var l int64
			if err := tx.QueryRow(ctx, sql, currentIdentity).Scan(&c, &l); err != nil {
				return fmt.Errorf("search: scanning pending %s: %w", entityType, err)
			}
			count += c
			length += l
		}
		return nil
	})
	if txErr != nil {
		return 0, 0, txErr
	}
	return count, length, nil
}
