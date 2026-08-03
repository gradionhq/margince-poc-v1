// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package org360

// The visit baseline: the per-user "I have seen this" mark, and the delta
// the company view reports against it.
//
// The mark moves forward ONLY through Acknowledge. A GET that advanced it
// as a side effect would destroy the answer the caller opened the page to
// read, and would make a prefetch indistinguishable from a visit.
//
// user_record_view is view state, not a record fact: it is written on
// every visit, no other user may read it, and no consumer can act on it.
// It therefore carries no audit row and no outbox event — the saved-view
// ruling, recorded against this package in backend/tableownership_test.go.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// entityTypeOrganization is the only record type the baseline covers
// today; the table's CHECK carries the same set.
const entityTypeOrganization = "organization"

// Acknowledge records that the calling human has now seen this account.
//
// The upsert takes GREATEST(stored, now), so a slow tab's late-arriving
// ack can never rewind a newer one — two tabs open on the same account
// converge on the later visit instead of racing the baseline backwards.
//
// The human gate is explicit and load-bearing, not defense in depth. An
// agent principal carries the granting human's id as its UserID
// (identity/passport.go stamps OnBehalfOf there for row scope), so
// "resolve the acting user" would happily write a baseline marking an
// account as SEEN by a human who never opened it — consuming their unread
// marker on their behalf. The transport's x-agent-access: human-only says
// the same thing one layer up; this is the layer that owns the write.
func (s *Service) Acknowledge(ctx context.Context, orgID ids.OrganizationID) (crmcontracts.RecordViewAck, error) {
	if err := auth.RequireHuman(ctx); err != nil {
		return crmcontracts.RecordViewAck{}, err
	}
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		return crmcontracts.RecordViewAck{}, err
	}
	userID, err := actingUser(ctx)
	if err != nil {
		return crmcontracts.RecordViewAck{}, err
	}
	now := s.now().UTC()
	var stored time.Time
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Anything that names a record is gated: acknowledging an account
		// the caller cannot read would confirm it exists.
		if err := auth.EnsureVisible(ctx, tx, "organization", orgID.UUID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			INSERT INTO user_record_view (workspace_id, user_id, entity_type, entity_id, last_viewed_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (workspace_id, user_id, entity_type, entity_id)
			DO UPDATE SET last_viewed_at = GREATEST(user_record_view.last_viewed_at, EXCLUDED.last_viewed_at)
			RETURNING last_viewed_at`,
			storekit.MustWorkspace(ctx), userID, entityTypeOrganization, orgID, now).Scan(&stored)
	})
	if err != nil {
		return crmcontracts.RecordViewAck{}, err
	}
	return crmcontracts.RecordViewAck{
		EntityType:   crmcontracts.RecordViewAckEntityTypeOrganization,
		EntityId:     openapi_types.UUID(orgID.UUID),
		LastViewedAt: stored,
	}, nil
}

// sinceLastVisit counts what changed on the account since the caller's own
// baseline. It is READ-ONLY: nothing here advances the mark.
//
// A caller with no stored baseline is on their first visit; the counts run
// over the account's whole history rather than over nothing, because "0
// new" on a record you have never opened is the wrong answer.
func (s *Service) sinceLastVisit(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, a *assembly) (crmcontracts.Organization360SinceLastVisit, error) {
	var out crmcontracts.Organization360SinceLastVisit
	since, visited, err := s.baselineFor(ctx, tx, orgID)
	if err != nil {
		return out, err
	}
	if visited {
		out.BaselineAt = &since
	}

	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos, sincePos := arg(orgID), arg(since)
	activityScope, err := auth.ActivityScopeClause(ctx, "a", arg)
	if err != nil {
		return out, err
	}
	if activityScope == "" {
		activityScope = scopeAll
	}
	// ONE statement, and it counts what the timeline shows: reachability is
	// activities.OrgLinkedActivityExists, so a mail filed against a contact at
	// the account is new here exactly when the page lists it as new. EXISTS
	// gives one row per activity, so the plain count needs no DISTINCT.
	if err := tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT count(*)
		FROM activity a
		WHERE a.archived_at IS NULL AND a.created_at > $%d AND %s AND (%s)`,
		sincePos, activities.OrgLinkedActivityExists(orgPos), activityScope),
		args...).Scan(&out.NewActivities); err != nil {
		return out, fmt.Errorf("count new activities: %w", err)
	}

	moves, counted, err := s.dealStageMoves(ctx, tx, orgID, since)
	if err != nil {
		return out, err
	}
	if counted {
		out.DealStageMoves = &moves
	}

	// The same decidable set the approvals section already read: counting it
	// again would run the whole scan and its per-kind grant checks twice
	// against one account in one transaction.
	staged, triageable, err := a.pendingApprovals()
	if err != nil {
		return out, err
	}
	if triageable {
		count := len(staged)
		out.PendingProposals = &count
	}

	return out, nil
}

// baselineFor reads the caller's own mark; visited is false when they
// have never acknowledged this account. The user_id predicate is explicit
// in SQL: RLS binds the workspace, so without it one rep would read
// another rep's reading history.
func (s *Service) baselineFor(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) (at time.Time, visited bool, err error) {
	userID, err := actingUser(ctx)
	if err != nil {
		return time.Time{}, false, err
	}
	err = tx.QueryRow(ctx, `
		SELECT last_viewed_at FROM user_record_view
		WHERE user_id = $1 AND entity_type = $2 AND entity_id = $3`,
		userID, entityTypeOrganization, orgID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return at, true, nil
}

// dealStageMoves counts stage changes on the account's deals since the
// baseline, over the caller's deal row scope. counted is false when the
// caller has no deal grant — not counted, which the contract keeps
// distinct from counted as zero.
func (s *Service) dealStageMoves(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID, since time.Time) (moves int, counted bool, err error) {
	if err := auth.Require(ctx, "deal", principal.ActionRead); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return 0, false, nil
		}
		return 0, false, err
	}
	var args []any
	arg := func(v any) int { args = append(args, v); return len(args) }
	orgPos, sincePos := arg(orgID), arg(since)
	dealScope, err := scopeClause(ctx, "deal", "d", arg)
	if err != nil {
		return 0, false, err
	}
	// deal_stage_history IS the stage-move ledger: one row per move, written
	// by the advance path, indexed on (deal_id, changed_at). A move with no
	// from_stage_id is the deal entering its first stage at creation — a new
	// deal, which the account reports as a new deal, not as a move.
	if err := tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT count(*)
		FROM deal_stage_history h
		JOIN deal d ON d.id = h.deal_id
		WHERE h.changed_at > $%d AND h.from_stage_id IS NOT NULL
		  AND d.organization_id = $%d AND d.archived_at IS NULL AND (%s)`,
		sincePos, orgPos, dealScope), args...).Scan(&moves); err != nil {
		return 0, false, fmt.Errorf("count deal stage moves: %w", err)
	}
	return moves, true, nil
}

// actingUser resolves the user this baseline belongs to. It answers for
// agents too — an agent's UserID is the granting human's — so it is a
// lookup, not a gate: Acknowledge's auth.RequireHuman is what keeps an
// agent from writing that human's mark. A principal with no user id at
// all (system, connector) has no baseline, and that is a refusal rather
// than a shared default row.
func actingUser(ctx context.Context) (ids.UserID, error) {
	p, ok := principal.Actor(ctx)
	if !ok || p.UserID == (ids.UUID{}) {
		return ids.UserID{}, fmt.Errorf(
			"the visit baseline and suggestion dismissals are both per-user, and this call carries no user: %w",
			apperrors.ErrPermissionDenied)
	}
	return ids.From[ids.UserKind](p.UserID), nil
}
