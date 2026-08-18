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
}

// pendingSources is the set-form counterpart to embedgen.go's embedText —
// one entry per embeddable entity, in the exact source-column shape that
// module maintains per-row. Adding a searchable entity means adding a row
// to BOTH maps; they must never diverge, since the pending count and the
// live indexer must agree on what "this entity's text" means.
var pendingSources = map[string]pendingSource{
	entityPerson:       {table: entityPerson, text: "t.full_name"},
	entityOrganization: {table: entityOrganization, text: "concat_ws(' ', t.display_name, t.legal_name, t.industry)"},
	entityDeal:         {table: entityDeal, text: "t.name"},
	entityLead:         {table: entityLead, text: "concat_ws(' ', t.full_name, t.company_name, t.title)"},
	entityActivity:     {table: entityActivity, text: "concat_ws(' ', t.subject, t.body)"},
	entityProject:      {table: entityProject, text: "concat_ws(' ', t.name, t.key, t.description)"},
}

// PendingByWorkspace is what a re-embed pass for each enumerated workspace will
// rebuild: live, non-empty-text embeddable entities lacking a current-identity
// embedding row. Since phase D every entry holds the SAME number — no entity
// table carries a tenant, so every pass rebuilds the installation's one corpus
// — and the map collapses to a single figure when the fan-out does. Do not sum
// it for a total; EntitiesPending says why. system-principal enumeration
// (mirrors embedgen.go:51-56): this is an index-maintenance rollup, not a
// user-facing read, so it must see every live entity regardless of any one
// caller's row scope.
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

// EntitiesPending is the installation's backlog, and it is deliberately NOT the
// sum of PendingByWorkspace. Since ADR-0091 §8 phase D no embeddable entity
// carries a tenant, so every workspace's rollup is the SAME set of rows;
// summing would multiply the backlog by the number of workspaces enumerated and
// report an installation with two of them as having twice the work. That number
// reaches an operator as `entities_pending` and prices the re-embed's token
// estimate, so the exaggeration would be a bill, not just a display.
//
// One pass, therefore, over the same query the per-workspace rollup runs. It is
// the second operand of ReindexNeeded's OR, which only asks whether the count
// is above zero — but the status read shows the figure itself.
func (s *Store) EntitiesPending(ctx context.Context, currentIdentity string) (int, error) {
	count, _, err := s.installationPending(ctx, currentIdentity)
	return count, err
}

// installationPending counts the backlog once, as the system principal and
// bound to no particular tenant — which is what every entity table now is.
func (s *Store) installationPending(ctx context.Context, currentIdentity string) (int, int64, error) {
	workspaces, err := s.fleetWorkspaceIDs(ctx)
	if err != nil {
		return 0, 0, err
	}
	if len(workspaces) == 0 {
		return 0, 0, nil
	}
	// Any workspace answers for the installation, because the query names none;
	// the binding exists only so the read has a store handle to run through.
	return s.forWorkspace(workspaces[0]).workspacePending(
		systemWorkspaceContext(ctx, workspaces[0].UUID), currentIdentity)
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
			// Every embeddable entity is installation-wide since ADR-0091 §8
			// phase D, so the backlog has no tenant to narrow to: one embedding
			// at the current identity covers a row wherever this rollup counts
			// it. The per-workspace shape survives only because the re-embed
			// fan-out still enqueues one pass per workspace; the two collapse
			// together.
			sql := fmt.Sprintf(`
				SELECT count(*), coalesce(sum(octet_length(btrim(%s))), 0)
				FROM %s t
				WHERE t.archived_at IS NULL
				  AND btrim(%s) <> ''
				  AND NOT EXISTS (
				        SELECT 1 FROM embedding e
				        WHERE e.entity_type = '%s' AND e.entity_id = t.id AND e.model = $1)`,
				src.text, src.table, src.text, entityType)
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
