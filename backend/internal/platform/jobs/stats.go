// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

// Reading the job table, rather than working it. Every SQL statement over
// river_job that serves a reader lives here, so the operational and the
// admin surface cannot drift into separate state, error and scoping
// vocabularies over one table that has no RLS to hold them together.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SweepTag marks a job row as one workspace's share of a fleet pass. Every
// site that fans work out to the fleet stamps it; a workspace job enqueued
// by a user action carries no tag and is correctly not counted as a fleet
// pass.
//
// Nothing enforces that a new fan-out remembers the tag — an untagged one
// is simply absent from the sweep gauges. Deriving that obligation needs a
// static notion of "this insert is a fan-out" the tree does not support
// today.
const SweepTag = "sweep"

// sweepTagPredicate is the tag test, spelled once. River stores tags as a
// text array, so membership is the array operator rather than a join.
const sweepTagPredicate = `'` + SweepTag + `' = ANY(tags)`

// runnableStates are the states a row can be worked FROM right now. The
// scheduled_at <= now() test that always accompanies this is what makes
// including 'scheduled' correct rather than misleading: a scheduled row
// whose time has passed IS runnable and unclaimed, and excluding it would
// let a stopped scheduler read as a perfectly healthy queue on the one
// gauge whose job is to catch exactly that. A row scheduled for the future
// fails the time test and contributes nothing, so no age is ever measured
// backwards.
const runnableStates = `('available','retryable','scheduled')`

// terminalBadStates are the states a workspace's pass can end in having
// done nothing. cancelled counts: a cancelled pass did not run, whatever
// the reason, and the sweep pair answers "are tenants being missed".
const terminalBadStates = `('discarded','cancelled')`

// StateRow is one (queue, kind, workspace, state) group of the job table as
// it stands right now. WorkspaceID is the empty string for a dispatcher —
// which is exact rather than a default, because a job that does tenant work
// declares its workspace and a null in that column means a dispatcher and
// nothing else (see role.go).
type StateRow struct {
	Queue       string
	Kind        string
	WorkspaceID string
	State       string
	Count       int64
	// OldestRunnableAgeSeconds is how long the oldest job in this group has
	// been ELIGIBLE and unclaimed. A scheduled job whose time has not come
	// is not stuck, so it contributes nothing; counting it would report a
	// nightly sweep as 23 hours overdue every day of its life.
	OldestRunnableAgeSeconds float64
}

// SweepPass is one fan-out kind read per workspace: how many workspaces it
// covers, and how many of those it is currently failing.
type SweepPass struct {
	Kind       string
	Workspaces int64
	Failed     int64
}

// Snapshot is one read of the job table, for a reader that renders it.
type Snapshot struct {
	Rows   []StateRow
	Sweeps []SweepPass
}

// Stats reads the live job table for the metric surface.
//
// TWO statements, not one. They read different populations — the runtime
// gauges must exclude completed rows (a finished job is history, not depth)
// while the sweep pair must include them (a pass whose workspaces all
// succeeded is the healthy case, and it is completed rows that say so).
// Folding them together needs a UNION with a discriminator and two nullable
// halves; two legible grouped scans inside one budget is the better trade.
//
// A caller that could not complete the read gets the error, never a partial
// or empty Snapshot: an unmeasured fleet renders identically to an idle one,
// and telling those apart is the whole point of the surface.
func Stats(ctx context.Context, pool *pgxpool.Pool) (Snapshot, error) {
	rows, err := statsByState(ctx, pool)
	if err != nil {
		return Snapshot{}, err
	}
	sweeps, err := statsBySweep(ctx, pool)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Rows: rows, Sweeps: sweeps}, nil
}

func statsByState(ctx context.Context, pool *pgxpool.Pool) ([]StateRow, error) {
	// The age is EXTRACTed from the database's own now(), never subtracted
	// from the app clock: the two clocks differ by enough to move an exact
	// assertion, which is a live intermittent flake elsewhere in this tree.
	const q = `
		SELECT queue,
		       kind,
		       coalesce(args->>'workspace_id', '') AS workspace_id,
		       state::text,
		       count(*)::bigint,
		       coalesce(
		           max(EXTRACT(EPOCH FROM (now() - scheduled_at)))
		               FILTER (WHERE state::text IN ` + runnableStates + `
		                         AND scheduled_at <= now()),
		           0)::double precision
		FROM river_job
		WHERE state <> 'completed'
		GROUP BY 1, 2, 3, 4`

	cursor, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("jobs: reading job state counts: %w", err)
	}
	defer cursor.Close()

	var out []StateRow
	for cursor.Next() {
		var r StateRow
		if err := cursor.Scan(&r.Queue, &r.Kind, &r.WorkspaceID, &r.State,
			&r.Count, &r.OldestRunnableAgeSeconds); err != nil {
			return nil, fmt.Errorf("jobs: scanning job state counts: %w", err)
		}
		out = append(out, r)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("jobs: reading job state counts: %w", err)
	}
	return out, nil
}

// statsBySweep reports, per fleet-pass kind, how many workspaces it covers
// and how many of those it is currently failing.
//
// It reads the LATEST OUTCOME PER WORKSPACE, never a batch. There is no
// such thing as "the last pass" in this table: River resolves a uniqueness
// conflict with ON CONFLICT DO UPDATE SET kind = EXCLUDED.kind, which
// writes neither created_at nor metadata, so a child still active from the
// previous pass is deduplicated and produces no row for the current one. A
// dispatcher retried while 90 of 100 children are live inserts 10 fresh
// rows — any batch-keyed reading, by timestamp or by a minted pass id,
// would report that fleet as 10.
//
// Per-workspace-latest also answers the question the pair exists for — are
// tenants being missed — more directly than a batch count did: a workspace
// whose most recent pass of a kind is dead is a tenant being missed,
// whether that happened this pass or three passes ago. And because it
// counts DISTINCT workspaces, a dispatcher that fans out per connection
// rather than per workspace still counts each workspace once, with no
// special case.
//
// The sweep tag is what separates a fleet pass from a workspace job someone
// triggered by hand. A dispatcher's own row carries no workspace and is
// excluded: it is not one workspace's share of anything.
func statsBySweep(ctx context.Context, pool *pgxpool.Pool) ([]SweepPass, error) {
	const q = `
		SELECT kind,
		       count(*)::bigint,
		       count(*) FILTER (WHERE state IN ` + terminalBadStates + `)::bigint
		FROM (
		    SELECT DISTINCT ON (kind, args->>'workspace_id')
		           kind, state::text AS state
		    FROM river_job
		    WHERE ` + sweepTagPredicate + `
		      AND args->>'workspace_id' IS NOT NULL
		    ORDER BY kind, args->>'workspace_id', created_at DESC, id DESC
		) latest
		GROUP BY kind`

	cursor, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("jobs: reading sweep passes: %w", err)
	}
	defer cursor.Close()

	var out []SweepPass
	for cursor.Next() {
		var s SweepPass
		if err := cursor.Scan(&s.Kind, &s.Workspaces, &s.Failed); err != nil {
			return nil, fmt.Errorf("jobs: scanning sweep passes: %w", err)
		}
		out = append(out, s)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("jobs: reading sweep passes: %w", err)
	}
	return out, nil
}
