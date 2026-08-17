// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// One MESSAGE's rungs, rather than one member's window.
//
// The window reads (ListMine, ListWorkspace) answer "what happened to my mail
// today" and filter to the funnel stages, because a non-funnel row is a rung on
// a message's ladder rather than a message of its own. This read is the other
// half: every stored rung for ONE message, funnel or not, which is what a member
// opens when the window's one-line answer is not enough.
//
// It is deliberately not a second spelling of the window's SQL. The window pages
// a member's whole 24 hours by keyset; this fetches a handful of rows for one
// natural key. Sharing a query would mean a filter that neither caller wants.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// TraceLadder is every stored rung for one message, with the identity needed to
// render them.
type TraceLadder struct {
	// Connector is the transport, as a provider id. The caller resolves the
	// display label through the channel-provider registry; storing one here
	// would make two deploys disagree about the same transport.
	Connector string

	// ActivityID is the row this message became, where it became one. An
	// internal-only drop writes no activity at all, which is exactly why this
	// read can be reached by a trace id as well as by an activity id.
	ActivityID *ids.UUID

	// Rungs are the stored rows, oldest stage first.
	Rungs []TraceRow

	// PayloadsEnabled reports the deployment's posture, so a reader can tell
	// "the operator did not turn this on" from "this row carried none".
	PayloadsEnabled bool
}

// LadderByTraceID answers for one row of the member's own window.
//
// The row's OWNER is the gate, not the row's id: capture_trace rows are the
// member's alone and no grant widens them (a workspace-owned binding's rows
// carry no member and take the capture_trace object instead). A caller who does
// not own the row gets ErrNotFound rather than a refusal — an existence proof is
// itself a disclosure about somebody else's mailbox.
func (s *TraceStore) LadderByTraceID(ctx context.Context, id ids.UUID, payloads bool) (TraceLadder, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return TraceLadder{}, fmt.Errorf("%w: this read answers for the calling member, and the invocation names none",
			errNoCallingMember)
	}
	shared := holdsSharedChannelGrant(ctx)
	return s.ladder(ctx, payloads, func(tx pgx.Tx) (traceAnchor, error) {
		return s.anchorByTraceID(ctx, tx, id, actor.UserID, shared)
	})
}

// LadderByActivityID answers for a message the caller can already open.
//
// The ACTIVITY's row scope is the caller's ticket to be here, and the compose
// layer has taken it before calling. The stored rungs still answer to their own
// owner: a colleague with a link-walk grant on the activity may read what the
// pipeline did to the RECORD, not what one member's connection recorded about
// their own mailbox. Rungs they may not see are withheld by the assembler, which
// is why this returns the owner alongside them.
func (s *TraceStore) LadderByActivityID(ctx context.Context, activityID ids.UUID, payloads bool) (TraceLadder, error) {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return TraceLadder{}, fmt.Errorf("%w: this read answers for the calling member, and the invocation names none",
			errNoCallingMember)
	}
	return s.ladder(ctx, payloads, func(tx pgx.Tx) (traceAnchor, error) {
		return s.anchorByActivityID(ctx, tx, activityID, actor.UserID)
	})
}

// traceAnchor is the natural key a message's rungs hang from. It exists before
// any activity does — the ingress gate and the internal-only drop both precede
// the activity write, and the drop never produces one at all.
type traceAnchor struct {
	sourceSystem string
	sourceID     string
	userID       ids.UUID
	connector    string
	activityID   *ids.UUID
}

func (s *TraceStore) ladder(ctx context.Context, payloads bool,
	resolve func(pgx.Tx) (traceAnchor, error),
) (TraceLadder, error) {
	var out TraceLadder
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		anchor, err := resolve(tx)
		if err != nil {
			return err
		}
		rungs, err := s.readRungs(ctx, tx, anchor)
		if err != nil {
			return err
		}
		out = TraceLadder{
			Connector:       anchor.connector,
			ActivityID:      anchor.activityID,
			Rungs:           rungs,
			PayloadsEnabled: payloads,
		}
		return nil
	})
	if err != nil {
		return TraceLadder{}, err
	}
	return out, nil
}

// anchorByTraceID resolves one row to the message it explains.
//
// The ownership predicate is IN the lookup rather than checked after it: a read
// that fetched the row and then compared owners would have already answered
// "this id exists", and the two branches are distinguishable by timing even when
// the response body is identical.
//
// `shared` widens it to rows a workspace-owned binding produced — a Telegram
// bot, a Zalo OA — which belong to no member and are the shared-channel tab's
// whole content. It NEVER widens to another member's rows: the predicate admits
// the caller's own id or a NULL owner, and nothing else. A grant reaching a
// colleague's mailbox is the one thing this table's design refuses.
func (s *TraceStore) anchorByTraceID(ctx context.Context, tx pgx.Tx, id, caller ids.UUID,
	shared bool,
) (traceAnchor, error) {
	var a traceAnchor
	var owner *ids.UUID
	err := tx.QueryRow(ctx, `
		SELECT source_system, source_id, user_id, connector, activity_id
		  FROM capture_trace
		 WHERE id = $1
		   AND (user_id = $2 OR ($3 AND user_id IS NULL))`, id, caller, shared).
		Scan(&a.sourceSystem, &a.sourceID, &owner, &a.connector, &a.activityID)
	if owner != nil {
		a.userID = *owner
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return traceAnchor{}, apperrors.ErrNotFound
	}
	if err != nil {
		return traceAnchor{}, fmt.Errorf("capture: resolving the trace ladder: %w", err)
	}
	return a, nil
}

// anchorByActivityID finds the message behind an activity the caller may read.
//
// Only the caller's OWN rows can anchor it. A colleague reading a shared record
// gets ErrNotFound here and the assembler renders the stored rungs as withheld —
// which keeps their place on the ladder without confirming that any exist.
func (s *TraceStore) anchorByActivityID(ctx context.Context, tx pgx.Tx, activityID, caller ids.UUID) (traceAnchor, error) {
	var a traceAnchor
	var owner *ids.UUID
	err := tx.QueryRow(ctx, `
		SELECT source_system, source_id, user_id, connector, activity_id
		  FROM capture_trace
		 WHERE activity_id = $1
		   AND user_id = $2
		 ORDER BY occurred_at
		 LIMIT 1`, activityID, caller).
		Scan(&a.sourceSystem, &a.sourceID, &owner, &a.connector, &a.activityID)
	if owner != nil {
		a.userID = *owner
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return traceAnchor{}, apperrors.ErrNotFound
	}
	if err != nil {
		return traceAnchor{}, fmt.Errorf("capture: resolving the trace ladder: %w", err)
	}
	return a, nil
}

// readRungs reads every stored row for one message, oldest first.
//
// No stage filter: this read wants the rungs the window deliberately hides. The
// window's 24-hour bound is not repeated either — the anchor row was found
// inside it, and its siblings share a natural key, so a bound here would only
// drop rungs whose sweep is imminent and make the ladder disagree with itself
// mid-request.
func (s *TraceStore) readRungs(ctx context.Context, tx pgx.Tx, a traceAnchor) ([]TraceRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT `+traceRowColumns+`
		  FROM capture_trace t`+resolutionJoin+`
		 WHERE t.source_system = $1 AND t.source_id = $2
		   AND t.user_id IS NOT DISTINCT FROM $3
		 ORDER BY t.occurred_at, t.id`, a.sourceSystem, a.sourceID, nullableID(a.userID))
	if err != nil {
		return nil, fmt.Errorf("capture: reading the trace ladder: %w", err)
	}
	defer rows.Close()
	var out []TraceRow
	for rows.Next() {
		r, err := scanTraceRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("capture: reading the trace ladder: %w", err)
	}
	return out, nil
}

// holdsSharedChannelGrant reports whether this caller may read rows belonging to
// a workspace-owned binding. It is the same gate ListWorkspace takes, asked here
// rather than re-decided, so the drawer and the list it opens from cannot
// disagree about who may see a shared channel.
func holdsSharedChannelGrant(ctx context.Context) bool {
	return auth.Require(ctx, traceObject, principal.ActionRead) == nil
}
