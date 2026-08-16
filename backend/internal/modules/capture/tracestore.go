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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/pipelinetrace"
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
	ID ids.UUID
	// Stage is which step of the pipeline recorded this row. The window reads
	// leave it as the funnel stage they filtered to; the ladder read uses it to
	// place each rung on the path.
	Stage      string
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
	if err := rows.Err(); err != nil {
		return fmt.Errorf("capture: reading the trace funnel: %w", err)
	}
	return nil
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
	rows, err := tx.Query(ctx, storekit.SQLf(`
		SELECT `+traceRowColumns+`
		  FROM capture_trace t`+resolutionJoin+`
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

// traceWhere is the window, the scope and the funnel filter, in that order, for
// the two WINDOW queries — the counters and the list.
//
// The funnel filter is here rather than at each call site because the two must
// agree by construction: a stage outside the funnel is one rung on a message's
// ladder, not a message of its own, so letting it into the list would show the
// same message twice while the counters above it disagreed with the rows below.
// The one-message ladder read deliberately does NOT use this — it wants every
// rung, which is the whole point of it.
func traceWhere(scope traceScope, addArg func(any) int) string {
	funnel := addArg(pipelinetrace.StageStrings(pipelinetrace.FunnelStages()))
	return fmt.Sprintf(
		`t.workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid
		   AND t.occurred_at > now() - make_interval(hours => %d)
		   AND t.stage = ANY($%d)
		   AND %s`, TraceWindowHours, funnel, scope.predicate(addArg))
}

// finishTracePage trims the lookahead row and mints the next cursor from it.
func finishTracePage(items []TraceRow, n int) ([]TraceRow, string) {
	if len(items) <= n {
		return items, ""
	}
	last := items[n-1]
	return items[:n], storekit.EncodeCursor(last.OccurredAt, last.ID)
}

// traceRowColumns and resolutionJoin are ONE spelling of "a trace row and what
// became of its sender", shared by the window read and the one-message ladder.
//
// Two spellings would be two answers: the window would say a sender is still
// waiting while the drawer opened from it said the verdict had landed, and a
// member comparing the two would be right to trust neither.
const traceRowColumns = `t.id, t.stage, t.connector, t.outcome, coalesce(t.reason, ''), t.activity_id,
		       d.status, coalesce(d.kind, ''), d.resolved_at,
		       coalesce(t.counterparty, ''), coalesce(t.subject, ''), t.occurred_at`

// resolutionJoin reaches a message's sender's disposition.
//
// A LATERAL taking ONE row, because the ledger holds a row per address per
// state: a plain join fans out and the same message appears once per historical
// disposition. Newest resolution first, with unresolved (NULL) ahead of it — an
// open question is the current answer.
//
// Joined through the ACTIVITY's counterparty address rather than through
// activity_id: the ledger keeps one open question per address and records the
// FIRST activity that raised it, so joining ids would answer only a sender's
// first message and leave their later ones reading "waiting on a verdict"
// forever after the verdict landed.
//
// Through the activity rather than t.counterparty because the trace holds no
// address unless an operator enabled payloads, and what a member is told must
// not depend on a diagnostic posture.
//
// A channel row reports a verdict only when the LADDER ITSELF opened the
// question, and the OUTCOME is what says so, because the transport cannot.
// kind='message' forces channel_provider non-null for every channel record, so
// that column says "this arrived on a channel" and never which ladder decided
// it — while two records that differ exactly there both reach this join. One
// names its human by a channel identity and takes decideChannelCounterparty,
// which writes no ledger row at all; an address riding along as corroboration
// must not make it inherit a mail verdict raised by somebody else's message.
// The other names its human by an ADDRESS alone — a mention, where the address
// IS the identity — runs the mail ladder like any mail, and defers a question
// that is its own to answer.
//
// LadderDispositionOutcomes is therefore the discriminator, and it holds only
// while a channel-identity record traces none of them. That is a property of a
// module this query cannot see, so it is gated where the writer is:
// TestChannelRecordSkipsEveryMailDomainGate drives the real Sink and asserts
// the outcome. Mail is unguarded, so a `captured` row carrying noise_prior or
// decided_prior still reports the settled PRIOR verdict that explains it.
//
// BOTH joins carry the workspace, and that is not belt-and-braces: there is no
// RLS on these tables since 0217, an address is not unique across tenants, and
// an unscoped `d.email = a.counterparty_email` would answer with ANOTHER
// workspace's verdict about the same person.
//
// The widened arm additionally requires a MEMBER, which keeps it on the
// personal side of the read. `a.channel_provider IS NULL` used to make that
// structural: a workspace-owned row could not reach the ledger through it at
// all. An outcome does not carry that property, so it is stated. A ledger row's
// owner_id is an individual, and ListWorkspace is the scope a manager holds a
// grant for — so without this, the day a workspace-owned binding emits an
// address-named record, that grant would start answering with dispositions
// raised by one member's own correspondence. A workspace row losing a verdict
// it could have shown is a gap on a screen; the other direction is a member's
// mail becoming readable by their manager.
//
// The outcome list is spelled as SQL literals so both queries stay compile-time
// constants; TestTheResolutionJoinSpellsEveryLadderDispositionOutcome holds the
// literals equal to LadderDispositionOutcomes, so a tier that starts recording a
// disposition cannot join that list and leave this join behind.
const resolutionJoin = `
		  LEFT JOIN activity a
		         ON a.id = t.activity_id AND a.workspace_id = t.workspace_id
		  LEFT JOIN LATERAL (
		         SELECT status, kind, resolved_at
		           FROM capture_pending_counterparty
		          WHERE workspace_id = t.workspace_id
		            AND email = a.counterparty_email
		            AND (a.channel_provider IS NULL
		                 OR (t.user_id IS NOT NULL
		                     AND t.outcome IN ('deferred', 'suppressed')))
		          ORDER BY resolved_at DESC NULLS FIRST
		          LIMIT 1) d ON true`

func scanTraceRow(rows pgx.Rows) (TraceRow, error) {
	var row TraceRow
	var status, kind *string
	var resolvedAt *time.Time
	if err := rows.Scan(&row.ID, &row.Stage, &row.Connector, &row.Outcome, &row.Reason, &row.ActivityID,
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
