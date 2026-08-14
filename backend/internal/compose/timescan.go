// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The clock-trigger cross-module edge (Task 14a): automation.TimeScanner
// over the ActivityScan seam, sourced from the activities module's own
// tables — injected here like every other cross-module edge (workflows.go,
// closedate.go, reconcile.go), never inside either module.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/customfields"
	"github.com/gradionhq/margince/backend/internal/platform/database"
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

// dateFieldScanAdapter maps automation.DateFieldScan onto
// customfields.Service.DateFieldCandidates — the one place this module's
// (EntityID, StoredValue, OccurrenceDate) row shape becomes the generic
// (EntityRef, Anchor) shape automation.DateFieldScan declares
// (ids/datasource/stdlib only, seams.go). Anchor rides OccurrenceDate,
// never StoredValue: for a recurring field OccurrenceDate is already
// projected onto the current scan window's year, which is the value
// renewal_reminder's occurrence-key (handlers_clock.go's
// anchorIdempotencyKey) must re-arm on.
type dateFieldScanAdapter struct {
	svc *customfields.Service
}

var _ automation.DateFieldScan = dateFieldScanAdapter{}

func (a dateFieldScanAdapter) Candidates(ctx context.Context, object, column string, from, to time.Time, recurring bool, limit int) ([]automation.DateFieldAnchor, error) {
	candidates, err := a.svc.DateFieldCandidates(ctx, object, column, from, to, recurring, limit)
	if err != nil {
		// A workspace admin can retire a custom field out from under an
		// automation instance that already named it (customfields.Retire):
		// save-time validation only checked non-emptiness, not live
		// existence. This is the one place both customfields'
		// ErrUnknownDateColumn and automation's own ErrDateFieldUnavailable
		// are in scope, so it is where the translation belongs — TimeScanner
		// (timescan.go's scanDateFieldInstanceCandidates) treats the
		// automation-side sentinel as this ONE instance's honest no-op
		// rather than failing the whole workspace's clock-scan pass.
		if errors.Is(err, customfields.ErrUnknownDateColumn) {
			return nil, fmt.Errorf("%w: %w", automation.ErrDateFieldUnavailable, err)
		}
		return nil, err
	}
	out := make([]automation.DateFieldAnchor, len(candidates))
	for i, c := range candidates {
		out[i] = automation.DateFieldAnchor{
			Ref:    datasource.EntityRef{Type: datasource.EntityType(object), ID: c.EntityID},
			Anchor: c.OccurrenceDate,
		}
	}
	return out, nil
}

// NewTimeScanner assembles the clock-trigger scanner for the worker
// process role: the SAME workflow engine and starter registration
// NewWorkflowEngine builds (so no_activity_reminder's Apply drives
// through the identical Executors every other starter uses), over the
// activities-sourced ActivityScan seam and the customfields-sourced
// DateFieldScan seam.
func NewTimeScanner(db *database.DB, log *slog.Logger) *automation.TimeScanner {
	return NewTimeScannerWithClock(db, time.Now, log)
}

// NewTimeScannerWithClock is NewTimeScanner with an explicit clock — the
// integration proof pins it so a scan pass evaluates "no activity for N
// days" (and a renewal date's own window) against seeded timestamps,
// never the wall clock.
func NewTimeScannerWithClock(db *database.DB, now func() time.Time, log *slog.Logger) *automation.TimeScanner {
	engine := NewWorkflowEngine(db)
	pool := db.Pool()
	return automation.NewTimeScannerWithClock(engine,
		activityScanAdapter{store: activities.NewStore(db)},
		dateFieldScanAdapter{svc: customfields.NewService(pool, nil)},
		now, log)
}
