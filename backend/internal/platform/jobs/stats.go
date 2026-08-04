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
	"maps"
	"slices"

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

// sweepTagPredicate is the tag test, spelled once. River stores tags in a
// varchar(255)[] column, so membership is the array operator rather than a
// join.
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
	Queue string
	Kind  string
	// WorkspaceID is the value of the args key VERBATIM. It is the empty
	// string only when the key is absent or JSON null — which is what
	// Untenanted reports, and the two must not be conflated: a row carrying
	// a present-but-EMPTY workspace_id is malformed, not a dispatcher, and
	// a reader that could not tell them apart would count it as one.
	WorkspaceID string
	// Untenanted is true when the workspace key is absent or null — the
	// exact test the scoped read's dispatcher arm uses, so the two surfaces
	// cannot disagree about which rows are fleet-wide.
	Untenanted bool
	State      string
	Count      int64
	// OldestRunnableAgeSeconds is how long the oldest job in this group has
	// been ELIGIBLE and unclaimed, and is NIL when the group holds no such
	// job. Nil and zero are different claims — nothing is runnable versus
	// something became runnable a moment ago — and flattening them would
	// report a queue of future-scheduled work as "the oldest runnable job
	// has waited 0 seconds", which is a statement about a job that does not
	// exist. The endpoint carries the same distinction.
	OldestRunnableAgeSeconds *float64
}

// SweepPass is one fan-out kind read per workspace: how many workspaces it
// covers, and how many of those it is currently failing.
type SweepPass struct {
	Kind       string
	Workspaces int64
	Failed     int64
}

// SweepUnit is one fan-out kind read per FAN-OUT UNIT — the grain the
// dispatcher actually ran the pass at — for the kinds whose unit is finer than
// a workspace.
//
// It exists because SweepPass counts workspaces, and three dispatchers fan out
// per CONNECTION and one per BUILD. A workspace holding two connections
// produces two children per pass, and if one fails while the other succeeds
// afterwards, the workspace's most recent child is the successful one: the
// failure is real, the tenant is being half-served, and the workspace pair
// reports nothing. Counting at the declared unit is what makes that visible.
//
// Only the kinds whose unit is NOT the workspace are reported. For the other
// twenty the unit key IS workspace_id, so this pair would restate
// SweepPass value for value; publishing both would be one number twice.
//
// The two families therefore OVERLAP rather than partition: a per-connection
// kind is reported by both, at two grains, because its rows carry a workspace
// id as well as a connection id. That is the point — the coarse reading
// answers fleet coverage and the fine one answers whether every unit ran —
// but it means the two must never be summed, and an alert reads whichever
// grain it means rather than adding them together.
type SweepUnit struct {
	Kind string
	Unit FanOutUnit
	// Units is how many distinct units of this kind have a surviving child,
	// and Failed how many of those most recently ended dead. The pair reads
	// exactly as SweepPass's does, one grain down.
	Units  int64
	Failed int64
}

// Snapshot is one read of the job table, for a reader that renders it.
type Snapshot struct {
	Rows   []StateRow
	Sweeps []SweepPass
	// Units is empty when no declared kind fans out below the workspace,
	// which is a legitimate fleet rather than a failed read — Stats returns
	// the error for that.
	Units []SweepUnit
}

// Stats reads the live job table for the metric surface.
//
// THREE statements, not one. They read different populations — the runtime
// gauges must exclude completed rows (a finished job is history, not depth)
// while both sweep reads must include them (a pass whose units all succeeded
// is the healthy case, and it is completed rows that say so) — and the two
// sweep reads group at different grains besides. Folding them together needs
// a UNION with a discriminator and several nullable halves; three legible
// grouped scans inside one budget is the better trade.
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
	units, err := statsBySweepUnit(ctx, pool)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Rows: rows, Sweeps: sweeps, Units: units}, nil
}

// subWorkspaceFanOuts answers the fan-out child kinds whose declared unit is
// finer than a workspace, paired with the args key that identifies one — the
// two arrays statsBySweepUnit joins the job table against.
//
// Derived from the contract on every call rather than kept as a list: a
// dispatcher that changes its fan_out_unit, or a new one that fans out per
// connection, is enrolled by the declaration that made it so. Both arrays are
// built in one pass so they stay index-aligned by construction.
func subWorkspaceFanOuts() (kinds, argsKeys []string) {
	units := FanOutUnits()
	for _, kind := range slices.Sorted(maps.Keys(units)) {
		key := units[kind].ArgsKey()
		if units[kind] == FanOutWorkspace || key == "" {
			continue
		}
		kinds = append(kinds, kind)
		argsKeys = append(argsKeys, key)
	}
	return kinds, argsKeys
}

func statsByState(ctx context.Context, pool *pgxpool.Pool) ([]StateRow, error) {
	// The age is EXTRACTed from the database's own now(), never subtracted
	// from the app clock: the two clocks differ by enough to move an exact
	// assertion, which is a live intermittent flake elsewhere in this tree.
	// The age has NO coalesce: a group with nothing runnable answers NULL,
	// which the reader carries as "no such job" rather than as a measured
	// zero. The workspace key is reported alongside a separate IS NULL
	// test, because `->>` yields the empty string for a present-but-empty
	// value as readily as coalesce does for an absent one — and those are a
	// malformed row and a dispatcher respectively.
	const q = `
		SELECT queue,
		       kind,
		       coalesce(args->>'workspace_id', '') AS workspace_id,
		       (args->>'workspace_id' IS NULL) AS untenanted,
		       state::text,
		       count(*)::bigint,
		       max(EXTRACT(EPOCH FROM (now() - scheduled_at)))
		           FILTER (WHERE state::text IN ` + runnableStates + `
		                     AND scheduled_at <= now())::double precision
		FROM river_job
		WHERE state <> 'completed'
		GROUP BY 1, 2, 3, 4, 5`

	cursor, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("jobs: reading job state counts: %w", err)
	}
	defer cursor.Close()

	var out []StateRow
	for cursor.Next() {
		var r StateRow
		if err := cursor.Scan(&r.Queue, &r.Kind, &r.WorkspaceID, &r.Untenanted,
			&r.State, &r.Count, &r.OldestRunnableAgeSeconds); err != nil {
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
// excluded: it is not one workspace's share of anything. A row whose
// workspace key is PRESENT but empty is excluded by the same test rather
// than counted as a workspace of its own — it is malformed, and a phantom
// tenant here would misreport how much of the fleet a pass actually covers.
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
		      AND coalesce(args->>'workspace_id', '') <> ''
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

// statsBySweepUnit is statsBySweep one grain down, for the kinds whose
// dispatcher fans out per connection or per build rather than per workspace.
//
// The reading rule is the SAME — latest outcome per unit, never a batch, for
// the reason statsBySweep sets out in full — and only the key it is latest PER
// changes: args->>'connection_id' instead of args->>'workspace_id'. That is
// what removes the masking. A workspace with a healthy connection and a broken
// one is one healthy unit and one failed unit here, where the workspace pair
// sees only whichever child ran last.
//
// The kind→key pairing arrives as two aligned arrays and is JOINED against
// rather than spliced into the SQL, so a kind name and an args key never reach
// the statement as text. The unnest join is also what bounds the scan to the
// declared sub-workspace kinds: a row of any other kind matches no pair and is
// not read.
//
// An empty pairing means the contract declares no such dispatcher, and the
// query is skipped rather than run with two empty arrays — the answer is the
// same, and the skip says why it is empty.
func statsBySweepUnit(ctx context.Context, pool *pgxpool.Pool) ([]SweepUnit, error) {
	kinds, argsKeys := subWorkspaceFanOuts()
	if len(kinds) == 0 {
		return nil, nil
	}

	const q = `
		SELECT kind,
		       count(*)::bigint,
		       count(*) FILTER (WHERE state IN ` + terminalBadStates + `)::bigint
		FROM (
		    SELECT DISTINCT ON (j.kind, j.args->>u.args_key)
		           j.kind, j.state::text AS state
		    FROM river_job j
		    JOIN unnest($1::text[], $2::text[]) AS u(kind, args_key) ON u.kind = j.kind
		    WHERE '` + SweepTag + `' = ANY(j.tags)
		      AND coalesce(j.args->>u.args_key, '') <> ''
		    ORDER BY j.kind, j.args->>u.args_key, j.created_at DESC, j.id DESC
		) latest
		GROUP BY kind`

	cursor, err := pool.Query(ctx, q, kinds, argsKeys)
	if err != nil {
		return nil, fmt.Errorf("jobs: reading sweep units: %w", err)
	}
	defer cursor.Close()

	units := FanOutUnits()
	var out []SweepUnit
	for cursor.Next() {
		var s SweepUnit
		if err := cursor.Scan(&s.Kind, &s.Units, &s.Failed); err != nil {
			return nil, fmt.Errorf("jobs: scanning sweep units: %w", err)
		}
		s.Unit = units[s.Kind]
		out = append(out, s)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("jobs: reading sweep units: %w", err)
	}
	return out, nil
}
