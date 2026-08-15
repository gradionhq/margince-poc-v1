// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// Reading the 24-hour trace: the funnel and one page of entries, for the
// caller's own connections or for the workspace's shared channels.
//
// The two reads differ in ONE predicate and share everything else, which is
// deliberate — the funnel and the list must never disagree about which rows they
// are describing. A count that included rows the list hides would leak by
// arithmetic what the list was careful not to show.
//
// There is no RLS behind any of this (0217, ADR-0091 §8), so every query below
// spells its own workspace predicate. §4 of that migration is blunt about the
// cost of forgetting: other users' rows rather than none, and no test failing.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TraceWindowHours is the window every read covers, and the only one. It is not
// a parameter: a caller choosing the window would be choosing how much of a
// swept table still exists, and the sweep answers that question already.
const TraceWindowHours = 24

// traceObject is the RBAC object governing the WORKSPACE read. The personal read
// is ungated on purpose — a member's own capture traffic is their own data, and
// there is no grant that widens it.
const traceObject = "capture_trace"

// errNoCallingMember is a personal read with nobody behind it — a job tick or a
// bus delivery. It is not a permission refusal: there is no member here to HAVE
// traffic, which a session-bound caller always has.
var errNoCallingMember = errors.New("capture: no calling member")

// TraceStore reads the trace. It writes nothing: the write is Trace, called from
// the pipeline on the transaction that made each decision.
type TraceStore struct {
	// db binds the workspace this store runs for (ADR-0091 §9 step 3).
	db *database.DB
}

// NewTraceStore builds the read store over the installation's pool.
func NewTraceStore(db *database.DB) *TraceStore { return &TraceStore{db: db} }

// TraceResolution is what later became of a deferred message's SENDER, read
// from the disposition ledger rather than copied into the trace.
type TraceResolution struct {
	Status     string
	Kind       string
	ResolvedAt *time.Time
}

// TraceRow is one entry as a client reads it.
type TraceRow struct {
	ID         ids.UUID
	Connector  string
	Outcome    string
	Reason     string
	ActivityID *ids.UUID
	Resolution *TraceResolution
	// Counterparty and Subject are empty unless the deployment enabled payload
	// capture, and are always empty for an erased subject.
	Counterparty string
	Subject      string
	OccurredAt   time.Time
}

// TraceWindow is the answer both reads give.
type TraceWindow struct {
	Funnel  map[string]int
	Entries []TraceRow
	Next    string
}

// ListMine answers for the caller's own connections.
func (s *TraceStore) ListMine(ctx context.Context, cursor *string, limit *int) (TraceWindow, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		// Not a refusal about permissions: there is no member here to have
		// traffic, which a session-bound caller always has.
		return TraceWindow{}, fmt.Errorf("%w: this read answers for the calling member, and the invocation names none",
			errNoCallingMember)
	}
	return s.window(ctx, traceScope{clause: "t.user_id = %s", arg: actor.UserID}, cursor, limit)
}

// ListWorkspace answers for connections the WORKSPACE owns — a bot binding whose
// traffic belongs to no single member.
//
// It selects `user_id IS NULL` and can express nothing else. A manager holding
// this grant reads shared-channel traffic; a member's own mailbox is personal
// data and no grant reaches it.
func (s *TraceStore) ListWorkspace(ctx context.Context, cursor *string, limit *int) (TraceWindow, error) {
	if err := auth.Require(ctx, traceObject, principal.ActionRead); err != nil {
		return TraceWindow{}, err
	}
	return s.window(ctx, traceScope{clause: "t.user_id IS NULL"}, cursor, limit)
}

// traceScope is the ONE predicate the two reads differ by. It is a value rather
// than two query builders so that the funnel and the page cannot be given
// different ones — the failure that would leak counts of rows the list hides.
type traceScope struct {
	clause string
	arg    ids.UUID
}

// predicate renders the scope, appending its argument when it has one.
func (sc traceScope) predicate(addArg func(any) int) string {
	if sc.arg.IsZero() {
		return sc.clause
	}
	return fmt.Sprintf(sc.clause, fmt.Sprintf("$%d", addArg(sc.arg)))
}

// window reads the funnel and one page under the same scope, in one transaction.
func (s *TraceStore) window(ctx context.Context, scope traceScope, cursor *string, limit *int) (TraceWindow, error) {
	n := storekit.ClampLimit(limit)
	out := TraceWindow{Funnel: map[string]int{}}
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := s.readFunnel(ctx, tx, scope, &out); err != nil {
			return err
		}
		rows, next, err := s.readPage(ctx, tx, scope, cursor, n)
		if err != nil {
			return err
		}
		out.Entries, out.Next = rows, next
		return s.hideUnreadableLinks(ctx, tx, out.Entries)
	})
	if err != nil {
		return TraceWindow{}, err
	}
	return out, nil
}

// readFunnel counts each outcome over the window, under the caller's scope.
func (s *TraceStore) readFunnel(ctx context.Context, tx pgx.Tx, scope traceScope, out *TraceWindow) error {
	args := []any{}
	addArg := func(v any) int { args = append(args, v); return len(args) }
	where := traceWhere(scope, addArg)
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT outcome, count(*) FROM capture_trace t WHERE %s GROUP BY outcome`, where), args...)
	if err != nil {
		return fmt.Errorf("capture: reading the trace funnel: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var outcome string
		var n int
		if err := rows.Scan(&outcome, &n); err != nil {
			return fmt.Errorf("capture: reading the trace funnel: %w", err)
		}
		out.Funnel[outcome] = n
	}
	return rows.Err()
}

// readPage reads one keyset page, newest first, joining the disposition ledger
// for what became of each deferred message's sender.
func (s *TraceStore) readPage(ctx context.Context, tx pgx.Tx, scope traceScope,
	cursor *string, n int,
) ([]TraceRow, string, error) {
	args := []any{}
	addArg := func(v any) int { args = append(args, v); return len(args) }
	where := traceWhere(scope, addArg)
	if cursor != nil && *cursor != "" {
		decoded, err := storekit.DecodeCursor(*cursor)
		if err != nil {
			return nil, "", err
		}
		where += fmt.Sprintf(" AND (t.occurred_at, t.id) < ($%d, $%d)",
			addArg(decoded.CreatedAt), addArg(decoded.ID))
	}
	// Resolution is joined through the ACTIVITY's counterparty address, not
	// through activity_id.
	//
	// The ledger keeps one open question per address and records the FIRST
	// activity that raised it, so joining ids answers only a sender's first
	// message: their second and later ones would read "waiting on a verdict"
	// forever, after the verdict had landed. One sender's answer covers every
	// message they sent, which is what this join says.
	//
	// Through the activity rather than through t.counterparty because the trace
	// holds no address unless an operator enabled payloads, and what a member is
	// told must not depend on a diagnostic posture. The activity is the member's
	// own message, so its sender's disposition is theirs to know.
	rows, err := tx.Query(ctx, storekit.SQLf(`
		SELECT t.id, t.connector, t.outcome, coalesce(t.reason, ''), t.activity_id,
		       d.status, coalesce(d.kind, ''), d.resolved_at,
		       coalesce(t.counterparty, ''), coalesce(t.subject, ''), t.occurred_at
		  FROM capture_trace t
		  LEFT JOIN activity a ON a.id = t.activity_id
		  LEFT JOIN capture_pending_counterparty d ON d.email = a.counterparty_email
		 WHERE %s
		 ORDER BY t.occurred_at DESC, t.id DESC
		 LIMIT %d`, where, n+1), args...)
	if err != nil {
		return nil, "", fmt.Errorf("capture: reading the trace page: %w", err)
	}
	defer rows.Close()
	items := make([]TraceRow, 0, n+1)
	for rows.Next() {
		row, err := scanTraceRow(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("capture: reading the trace page: %w", err)
	}
	page, next := finishTracePage(items, n)
	return page, next, nil
}

// traceWhere is the window and the scope, in that order, for every query here.
func traceWhere(scope traceScope, addArg func(any) int) string {
	return fmt.Sprintf(
		`t.workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		   AND t.occurred_at > now() - make_interval(hours => %d)
		   AND %s`, TraceWindowHours, scope.predicate(addArg))
}

// finishTracePage trims the lookahead row and mints the next cursor from it.
func finishTracePage(items []TraceRow, n int) ([]TraceRow, string) {
	if len(items) <= n {
		return items, ""
	}
	last := items[n-1]
	return items[:n], storekit.EncodeCursor(last.OccurredAt, last.ID)
}

func scanTraceRow(rows pgx.Rows) (TraceRow, error) {
	var row TraceRow
	var status, kind *string
	var resolvedAt *time.Time
	if err := rows.Scan(&row.ID, &row.Connector, &row.Outcome, &row.Reason, &row.ActivityID,
		&status, &kind, &resolvedAt, &row.Counterparty, &row.Subject, &row.OccurredAt); err != nil {
		return TraceRow{}, fmt.Errorf("capture: reading the trace page: %w", err)
	}
	if status != nil {
		row.Resolution = &TraceResolution{Status: *status, ResolvedAt: resolvedAt}
		if kind != nil {
			row.Resolution.Kind = *kind
		}
	}
	return row, nil
}

// hideUnreadableLinks drops the activity link from every entry whose activity
// the caller may not read.
//
// The trace row is theirs — it describes their own message — but the row it
// points at can move out of their scope afterwards, and returning the id would
// make this surface an existence oracle over rows the timeline itself would
// refuse. The entry still lists, with no link, which is the honest answer.
//
// ONE query per page rather than one per row: a probe per entry is up to `limit`
// round trips on a read a member refreshes.
func (s *TraceStore) hideUnreadableLinks(ctx context.Context, tx pgx.Tx, entries []TraceRow) error {
	linked := make([]ids.UUID, 0, len(entries))
	for _, e := range entries {
		if e.ActivityID != nil {
			linked = append(linked, *e.ActivityID)
		}
	}
	if len(linked) == 0 {
		return nil
	}
	args := []any{linked}
	addArg := func(v any) int { args = append(args, v); return len(args) }
	// ActivityScopeClause, not the generic one: an activity has no owner, so it
	// inherits the sensitivity of the records it attaches to — visible when ANY
	// linked person, organization or deal is, and visible to everyone when it
	// has no links at all (a workspace-shared note). The generic clause refuses
	// this table outright, which is how the wrong one announces itself.
	scope, err := auth.ActivityScopeClause(ctx, "a", addArg)
	if err != nil {
		return err
	}
	if scope == "" {
		// An unbounded reader sees every one of them; the probe would be a
		// round trip to learn nothing.
		return nil
	}
	rows, err := tx.Query(ctx, storekit.SQLf(
		`SELECT a.id FROM activity a WHERE a.id = ANY($1) AND %s`, scope), args...)
	if err != nil {
		return fmt.Errorf("capture: checking which linked activities are readable: %w", err)
	}
	defer rows.Close()
	readable := map[ids.UUID]bool{}
	for rows.Next() {
		var id ids.UUID
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("capture: checking which linked activities are readable: %w", err)
		}
		readable[id] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("capture: checking which linked activities are readable: %w", err)
	}
	for i := range entries {
		if entries[i].ActivityID != nil && !readable[*entries[i].ActivityID] {
			entries[i].ActivityID = nil
		}
	}
	return nil
}
