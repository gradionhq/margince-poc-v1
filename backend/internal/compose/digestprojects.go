// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The morning digest's projects section, answered for the capture module
// (capture.DigestProjectsSource). It lives here because the three reads span
// the deals module's tables and rules — the phase ladder's history, the tasks
// filed under a project, and the quiet rule (deals.ProjectQuietSQL) the
// projects-gone-quiet report and the project_gone_quiet signal already share
// — and a module never imports a sibling.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// digestProjectsSource is the seam capture's nightly build calls, once per
// reader under that reader's principal. No project grant means no section;
// with one, every query below carries the reader's project row scope, and
// the commitment count carries their activity content clause, so the digest
// says nothing a page of theirs would withhold.
func digestProjectsSource(ctx context.Context, tx pgx.Tx, since, now time.Time) (*capture.DigestProjects, error) {
	if err := auth.Require(ctx, tableProject, principal.ActionRead); err != nil {
		if errors.Is(err, apperrors.ErrPermissionDenied) {
			return nil, nil //nolint:nilnil // deliberate: no grant is "no section", not a fault
		}
		return nil, err
	}
	changes, err := digestPhaseChanges(ctx, tx, since)
	if err != nil {
		return nil, err
	}
	commitments, err := digestNewCommitments(ctx, tx, since)
	if err != nil {
		return nil, err
	}
	quiet, err := digestQuietProjects(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	return &capture.DigestProjects{PhaseChanges: changes, NewCommitments: commitments, GoneQuiet: quiet}, nil
}

// projectScope renders the reader's project row scope over alias p, or TRUE
// for an unbounded reader, binding through arg.
func projectScope(ctx context.Context, arg func(any) int) (string, error) {
	scope, err := auth.ScopeClauseFor(ctx, tableProject, "p", arg)
	if err != nil {
		return "", err
	}
	if scope == "" {
		scope = sqlUnnarrowed
	}
	return scope, nil
}

// digestPhaseChanges reads the ladder moves recorded in the window off
// project_phase_history — the writer's record, never re-derived from the
// row's current phase, which would miss a project that moved twice.
func digestPhaseChanges(ctx context.Context, tx pgx.Tx, since time.Time) ([]capture.DigestProjectPhaseChange, error) {
	args := []any{since}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := projectScope(ctx, arg)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.name, p.key, h.from_phase, h.to_phase, h.occurred_at
		  FROM project_phase_history h
		  JOIN project p ON p.id = h.project_id AND p.archived_at IS NULL
		 WHERE h.occurred_at >= $1 AND `+scope+`
		 ORDER BY h.occurred_at DESC, h.id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("digest phase changes: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (capture.DigestProjectPhaseChange, error) {
		var change capture.DigestProjectPhaseChange
		err := row.Scan(&change.ProjectID, &change.Name, &change.Key, &change.FromPhase, &change.ToPhase, &change.OccurredAt)
		return change, err
	})
}

// digestNewCommitments counts, per project, the tasks filed under it in the
// window that are still open — a promise made overnight and already kept is
// not something the morning reader has to act on.
func digestNewCommitments(ctx context.Context, tx pgx.Tx, since time.Time) ([]capture.DigestProjectCommitments, error) {
	args := []any{since}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := projectScope(ctx, arg)
	if err != nil {
		return nil, err
	}
	// The tasks themselves are read through the reader's activity content
	// clause — a promise filed in correspondence they may not open is not
	// theirs to count.
	content, err := auth.ActivityContentClause(ctx, "a", arg)
	if err != nil {
		return nil, err
	}
	if content == "" {
		content = sqlUnnarrowed
	}
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.name, p.key, count(*)::int
		  FROM activity a
		  JOIN activity_link al ON al.activity_id = a.id AND al.entity_type = 'project'
		  JOIN project p ON p.id = al.project_id AND p.archived_at IS NULL
		 WHERE a.kind = 'task' AND NOT a.is_done AND a.archived_at IS NULL
		   AND a.created_at >= $1 AND `+scope+` AND `+content+`
		 GROUP BY p.id, p.name, p.key
		 ORDER BY count(*) DESC, p.name, p.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("digest new commitments: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (capture.DigestProjectCommitments, error) {
		var item capture.DigestProjectCommitments
		err := row.Scan(&item.ProjectID, &item.Name, &item.Key, &item.NewOpenCommitments)
		return item, err
	})
}

// digestQuietProjects lists the projects the quiet rule fires on at `now`,
// through the predicate the report and the signal use, quietest first.
func digestQuietProjects(ctx context.Context, tx pgx.Tx, now time.Time) ([]capture.DigestProjectQuiet, error) {
	args := []any{now, deals.DefaultProjectQuietDays}
	arg := func(v any) int { args = append(args, v); return len(args) }
	scope, err := projectScope(ctx, arg)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT p.id, p.name, p.key, p.phase, p.owner_id, `+deals.ProjectQuietAnchorSQL("p")+`
		  FROM project p
		 WHERE p.archived_at IS NULL
		   AND `+deals.ProjectInFlightSQL("p")+`
		   AND `+deals.ProjectQuietSQL("p", "$1", 2)+`
		   AND `+scope+`
		 ORDER BY `+deals.ProjectQuietAnchorSQL("p")+`, p.id`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("digest quiet projects: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (capture.DigestProjectQuiet, error) {
		var item capture.DigestProjectQuiet
		if err := row.Scan(&item.ProjectID, &item.Name, &item.Key, &item.Phase, &item.OwnerID, &item.QuietSince); err != nil {
			return item, err
		}
		item.DaysQuiet = int(now.Sub(item.QuietSince).Hours() / 24)
		return item, nil
	})
}
