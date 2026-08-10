// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package auth

// Row scope for the records that own no owner_id. An activity and a
// signal carry free text about OTHER records, so their visibility is
// inherited rather than held: an activity from the records its links
// point at, a signal from the record it is about. Both rules live here,
// next to the activity_link disjunction they share (linkscope.go),
// because scope policy has exactly one spelling (ADR-0054 §8).

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ActivityScopeClause is the activity analogue of ScopeClause:
// activities have no owner, but their free-text inherits the
// sensitivity of the records they attach to. An activity is visible when
// ANY linked person/organization/deal is visible under the caller's row
// scope, or when it has no links at all (a workspace-shared note).
// It lives here, not in a module: it is the one scope
// rule that spans person, organization, deal and activity_link rows, and
// both the activities timeline and people's promotion-evidence check
// enforce it — scope policy has exactly one spelling (ADR-0054 §8).
// alias names the activity table in the outer query.
func ActivityScopeClause(ctx context.Context, alias string, arg func(any) int) (string, error) {
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	if UnboundedFor(p, linkTargetTables...) {
		return "", nil
	}
	// One correlated pass over the activity's links, not two. bool_or is
	// exactly the ANY-link rule, and its empty-set answer — NULL, coalesced
	// to true — is exactly the link-less note rule, so the shape carries
	// both halves without a second subquery.
	//
	// The two-subquery spelling this replaces read more plainly but cost a
	// hashed subplan: Postgres materialized the WHOLE activity_link table
	// up front to answer the NOT EXISTS arm, ~180ms on the smb bench tier,
	// paid before a single candidate row was examined. Every row-scoped
	// caller has been paying it; it only surfaced as a budget failure when
	// capture privacy stopped exempting the all-scope reader the perf
	// fixture happens to run as.
	return fmt.Sprintf(`coalesce((SELECT bool_or(%[2]s)
	   FROM activity_link l WHERE l.activity_id = %[1]s.id), true)`,
		alias, linkTargetVisible(p, "l", arg)), nil
}

// SignalScopeClause is the signal analogue of ActivityScopeClause: a
// A signal is visible when its SUBJECT is visible and its own visibility
// admits the reader. A subject-less signal (a raw item still awaiting
// resolution) is workspace-shared, like an unlinked note.
//
// The subject arm is the older half: a signal's free-text summary and evidence
// inherit the sensitivity of the record it is ABOUT. That was the whole rule
// while a signal's evidence could only come from records at least as visible as
// its subject — the producers reached an account through a direct activity_link
// row, which is the same link that makes an activity readable to that account's
// readers (ActivityScopeClause is the any-link rule).
//
// The producers now also reach an account through the employer of the contact a
// message is filed against, and through its deal. Neither is a link on the
// activity, so a signal's evidence can be narrower than its subject, and the
// signal carries its own visibility to say so. It is capture privacy, so it
// does NOT yield to row_scope=all: an admin reading a colleague's unpromoted
// correspondence through a summary of it is the same disclosure the boundary
// exists to prevent, taking the long way round.
//
// It lives here, not in the signals module, because the signals store's reads
// and the approvals surface's staged-archive visibility probe both enforce it
// — scope policy has exactly one spelling (ADR-0054 §8). alias names the
// signal table in the outer query.
func SignalScopeClause(ctx context.Context, alias string, arg func(any) int) (string, error) {
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	// The system principal — the producers themselves, the relay, the privacy
	// engines — reads both arms away. Everyone else faces the private arm,
	// unbounded or not.
	if p.Type == principal.PrincipalSystem {
		return "", nil
	}
	private := fmt.Sprintf("(%[1]s.visibility <> 'owner' OR %[1]s.owner_id = $%d)",
		alias, arg(p.UserID))
	if UnboundedFor(p, "person", "organization", "deal") {
		return private, nil
	}
	person := VisiblePredicate(p, "person", arg)
	organization := VisiblePredicate(p, "organization", arg)
	deal := VisiblePredicate(p, "deal", arg)
	return fmt.Sprintf(`(%[5]s AND (%[1]s.entity_type IS NULL
	 OR (%[1]s.entity_type = 'person'       AND EXISTS (SELECT 1 FROM person sp WHERE sp.id = %[1]s.entity_id AND %[2]s))
	 OR (%[1]s.entity_type = 'organization' AND EXISTS (SELECT 1 FROM organization so WHERE so.id = %[1]s.entity_id AND %[3]s))
	 OR (%[1]s.entity_type = 'deal'         AND EXISTS (SELECT 1 FROM deal sd WHERE sd.id = %[1]s.entity_id AND %[4]s))))`,
		alias, person("sp"), organization("so"), deal("sd"), private), nil
}

// EnsureSignalVisible is EnsureVisible for signals, using the
// subject-entity scope above; out of scope reads as ErrNotFound.
func EnsureSignalVisible(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := SignalScopeClause(ctx, "s", arg)
	if err != nil {
		return err
	}
	if clause == "" {
		return nil
	}
	var visible bool
	err = tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM signal s WHERE s.id = $%d AND %s)`, idPos, clause),
		args...).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// EnsureSignalVisibleLive is EnsureSignalVisible with the two strictnesses a
// caller serving STORED data needs — the row must still be live, and an
// unbounded actor does not skip the probe. See EnsureVisibleLive.
func EnsureSignalVisibleLive(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := SignalScopeClause(ctx, "s", arg)
	if err != nil {
		return err
	}
	return probeExistsLive(ctx, tx, "signal s", "s", idPos, clause, args)
}

// EnsureActivityVisible is EnsureVisible for activities, using the
// linked-entity scope above; out of scope reads as ErrNotFound.
func EnsureActivityVisible(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return err
	}
	if clause == "" {
		return nil
	}
	var visible bool
	err = tx.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM activity a WHERE a.id = $%d AND %s)`, idPos, clause),
		args...).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// EnsureActivityVisibleLive is EnsureActivityVisible with the two
// strictnesses a caller serving STORED data needs — the row must still be
// live, and an unbounded actor does not skip the probe. See EnsureVisibleLive.
func EnsureActivityVisibleLive(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return err
	}
	return probeExistsLive(ctx, tx, "activity a", "a", idPos, clause, args)
}

// probeExistsLive is the one spelling of "this row exists, is not archived,
// and the caller may see it" for the aliased scope probes. An empty clause
// narrows nothing but never SKIPS the probe: that skip is what would let an
// unbounded actor be handed a row that is gone.
func probeExistsLive(ctx context.Context, tx pgx.Tx, from, alias string, idPos int, clause string, args []any) error {
	q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE %s.id = $%d AND %s.archived_at IS NULL`,
		from, alias, idPos, alias)
	if clause != "" {
		q += " AND " + clause
	}
	q += ")"

	var visible bool
	if err := tx.QueryRow(ctx, q, args...).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}

// RelationshipEndpointScope is the edge analogue: a relationship owns no
// owner_id, and its sensitivity is the CONJUNCTION of its endpoints' — an edge
// names two records, so one readable by someone who cannot read either end would
// disclose that record's existence and its link to the other.
//
// Every non-null endpoint must be visible under the caller's row scope, on read
// exactly as on write. Only a caller unbounded over EVERY endpoint table carries
// no clause; person and organization hold capture privacy, so that is the system
// principal alone.
//
// It lives here rather than in people for the reason the two rules above do:
// scope policy has exactly one spelling (ADR-0054 §8), and this rule now has two
// readers in different modules — people's own list and read SQL, and the
// approvals inbox, which must decide whether a staged archive of an edge is
// visible to the human being asked to approve it. A second copy of a conjunction
// is a second place for one of its five arms to be forgotten.
//
// alias names the relationship table in the outer query.
func RelationshipEndpointScope(ctx context.Context, alias string, arg func(any) int) (string, error) {
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	if UnboundedFor(p, relationshipEndpoints()...) {
		return "", nil
	}
	clauses := make([]string, 0, len(relationshipEndpointColumns))
	for _, endpoint := range relationshipEndpointColumns {
		predicate := VisiblePredicate(p, endpoint.table, arg)
		clauses = append(clauses, fmt.Sprintf(
			`(%[1]s.%[2]s IS NULL OR EXISTS (
			   SELECT 1 FROM %[3]s ep WHERE ep.id = %[1]s.%[2]s AND ep.archived_at IS NULL AND %[4]s))`,
			alias, endpoint.column, endpoint.table, predicate("ep"),
		))
	}
	return "(" + strings.Join(clauses, " AND ") + ")", nil
}

// relationshipEndpointColumns is every endpoint an edge can carry, paired with
// the table it points at. Two columns point at `organization`, which is why this
// is a slice and not a map.
var relationshipEndpointColumns = []struct{ column, table string }{
	{"person_id", tablePerson},
	{"organization_id", tableOrganization},
	{"counterparty_org_id", tableOrganization},
	{"deal_id", tableDeal},
	{"project_id", tableProject},
}

// relationshipEndpoints is the distinct endpoint TABLES, for the unbounded
// short-circuit. Derived from the column list so the two cannot disagree about
// which tables an edge can reach.
func relationshipEndpoints() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(relationshipEndpointColumns))
	for _, endpoint := range relationshipEndpointColumns {
		if seen[endpoint.table] {
			continue
		}
		seen[endpoint.table] = true
		out = append(out, endpoint.table)
	}
	return out
}

// EnsureRelationshipVisible probes one edge under the endpoint-conjunction rule.
// Absence and out-of-scope answer identically (existence-hiding), which is what
// lets the approvals inbox ask "may this human see the edge this archive targets"
// without disclosing that the edge exists.
//
// Archived edges still answer visible, matching the clause the owning store uses:
// an approval staged against an edge that was archived in the meantime stays
// DECIDABLE, so a human can reject it rather than find an undecidable row.
func EnsureRelationshipVisible(ctx context.Context, tx pgx.Tx, id ids.UUID) error {
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	idPos := arg(id)

	clause, err := RelationshipEndpointScope(ctx, "r", arg)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM relationship r WHERE r.id = $%d`, idPos)
	if clause != "" {
		q += " AND " + clause
	}
	q += ")"

	var visible bool
	if err := tx.QueryRow(ctx, q, args...).Scan(&visible); err != nil {
		return err
	}
	if !visible {
		return apperrors.ErrNotFound
	}
	return nil
}
