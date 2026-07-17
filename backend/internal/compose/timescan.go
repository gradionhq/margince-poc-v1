// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The clock-trigger cross-module edge (Task 14a): automation.TimeScanner
// over the ActivityScan seam, sourced from the activities module's own
// tables — injected here like every other cross-module edge (workflows.go,
// closedate.go, reconcile.go), never inside either module.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// activityScanAdapter maps automation.ActivityScan onto
// activities.Store.LastTouchBefore — the one place this module's own
// activity_link entity-type strings become the generic (EntityRef,
// anchor) shape the automation module's seam declares (ids/datasource/
// stdlib only, seams.go).
type activityScanAdapter struct {
	store *activities.Store
}

var _ automation.ActivityScan = activityScanAdapter{}

func (a activityScanAdapter) LastTouchBefore(ctx context.Context, cutoff time.Time, limit int) ([]automation.EntityAnchor, error) {
	candidates, err := a.store.LastTouchBefore(ctx, cutoff, limit)
	if err != nil {
		return nil, err
	}
	out := make([]automation.EntityAnchor, len(candidates))
	for i, c := range candidates {
		out[i] = automation.EntityAnchor{
			Ref:    datasource.EntityRef{Type: datasource.EntityType(c.EntityType), ID: c.EntityID},
			Anchor: c.LastTouch,
		}
	}
	return out, nil
}

// NewTimeScanner assembles the clock-trigger scanner for the worker
// process role: the SAME workflow engine and starter registration
// NewWorkflowEngine builds (so no_activity_reminder's Apply drives
// through the identical Executors every other starter uses), over the
// activities-sourced ActivityScan seam.
func NewTimeScanner(pool *pgxpool.Pool, log *slog.Logger) *automation.TimeScanner {
	return NewTimeScannerWithClock(pool, time.Now, log)
}

// NewTimeScannerWithClock is NewTimeScanner with an explicit clock — the
// integration proof pins it so a scan pass evaluates "no activity for N
// days" against seeded timestamps, never the wall clock.
func NewTimeScannerWithClock(pool *pgxpool.Pool, now func() time.Time, log *slog.Logger) *automation.TimeScanner {
	engine := NewWorkflowEngine(pool)
	return automation.NewTimeScannerWithClock(engine, activityScanAdapter{store: activities.NewStore(pool)}, now, log)
}
