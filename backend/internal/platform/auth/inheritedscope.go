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

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
// signal has no owner_id — its free-text summary/evidence inherit the
// sensitivity of the record it is ABOUT, so a signal is visible when its
// subject entity (entity_type/entity_id) is visible under the caller's
// row scope. A subject-less signal (a raw item still awaiting resolution)
// is workspace-shared, like an unlinked note. It lives
// here, not in the signals module, because the signals store's reads and
// the approvals surface's staged-archive visibility probe both enforce it
// — scope policy has exactly one spelling (ADR-0054 §8). alias names the
// signal table in the outer query.
func SignalScopeClause(ctx context.Context, alias string, arg func(any) int) (string, error) {
	p, err := rbacActor(ctx)
	if err != nil {
		return "", err
	}
	if UnboundedFor(p, "person", "organization", "deal") {
		return "", nil
	}
	person := VisiblePredicate(p, "person", arg)
	organization := VisiblePredicate(p, "organization", arg)
	deal := VisiblePredicate(p, "deal", arg)
	return fmt.Sprintf(`(%[1]s.entity_type IS NULL
	 OR (%[1]s.entity_type = 'person'       AND EXISTS (SELECT 1 FROM person sp WHERE sp.id = %[1]s.entity_id AND %[2]s))
	 OR (%[1]s.entity_type = 'organization' AND EXISTS (SELECT 1 FROM organization so WHERE so.id = %[1]s.entity_id AND %[3]s))
	 OR (%[1]s.entity_type = 'deal'         AND EXISTS (SELECT 1 FROM deal sd WHERE sd.id = %[1]s.entity_id AND %[4]s)))`,
		alias, person("sp"), organization("so"), deal("sd")), nil
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
