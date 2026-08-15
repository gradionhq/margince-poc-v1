// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The scheduled-send driver the integration lane needs and nothing else does.
// It lives behind the integration tag so it is never linked into cmd/api or
// cmd/worker: the lane compiles this package with the tag, so it drives the
// REAL worker, while the shipped binaries carry no exported surface with no
// product caller.

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// DriveScheduledSendForTest wakes one scheduled message through the production
// worker, exactly as its River alarm would.
//
// The worker is assembled the way the worker role assembles it, so the
// authority rebuild, the live gate re-run and the single-transaction fire are
// all the ones that ship. A lane that hand-rolled the fire would prove its own
// copy works and say nothing about the product.
//
// A snooze is not an error: a message whose moment has moved reports one, and
// the lane reads the ROW to see what happened rather than the return.
func DriveScheduledSendForTest(ctx context.Context, pool *pgxpool.Pool, workspace, id ids.UUID) error {
	inserter, err := jobs.NewInserter(pool, slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	worker := newScheduledSendWorker(pool, NewDeliveryStager(pool, inserter), nil, SendPacing{})
	err = worker.Work(ctx, &river.Job[ScheduledSendArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: scheduledSendMaxAttempts},
		Args:   ScheduledSendArgs{Workspace: workspace, ScheduledSendID: id.String()},
	})
	var snooze *river.JobSnoozeError
	if errors.As(err, &snooze) {
		// Its moment moved: the alarm asked to come back later, which is an
		// outcome and not a failure.
		return nil
	}
	return err
}

// HoldScheduledSendForTest drives the store's hold under an observed row
// version, standing in for a worker whose attempt failed and is now holding
// what it saw. The lane needs it to prove a STALE observation declines.
func HoldScheduledSendForTest(ctx context.Context, pool *pgxpool.Pool, workspace, id ids.UUID, reason string, observed int64) error {
	store := sendStore(pool, SendPath{})
	return store.HoldScheduledSend(sendWorkerScope(principal.WithWorkspaceID(ctx, workspace)), id, reason, observed)
}

// DriveScheduledSendRecoveryForTest runs one recovery pass through the
// PRODUCTION worker, on the bare context River actually hands it.
//
// No workspace is injected, deliberately: River gives this periodic job no
// tenant, so a helper that supplied one would prove the HELPER works while
// production resolved nothing. The worker resolves the installation itself,
// which is the behaviour under test.
func DriveScheduledSendRecoveryForTest(ctx context.Context, pool *pgxpool.Pool) error {
	inserter, err := jobs.NewInserter(pool, slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	worker := newScheduledSendRecoveryWorker(
		identity.NewService(pool), sendStore(pool, SendPath{}), NewScheduleTimer(inserter), slog.New(slog.DiscardHandler))
	return worker.Work(ctx, &river.Job[ScheduledSendRecoveryArgs]{
		JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 1},
	})
}
