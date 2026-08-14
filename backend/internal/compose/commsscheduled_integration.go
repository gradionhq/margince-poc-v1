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

	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
