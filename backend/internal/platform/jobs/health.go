// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

// The scoped read behind GET /admin/job-health. It lives beside Stats
// rather than in the composition layer because river_job has no RLS and no
// workspace column: every statement over it is a hand-imposed scope, and
// two readers spelling that scope in two packages is how the operational
// and the admin surface drift into different answers about the same table.

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// waitingStates are queued-and-not-yet-started. pending is River's staged
// state; it is counted here for the same reason the metric counts it —
// work nobody has done is waiting, whatever River calls the state.
const waitingStates = `('available','scheduled','pending')`

// healthScope is the row filter this read imposes for itself.
//
// river_job has no workspace_id COLUMN, so it has no RLS and no row-scope
// clause can be inherited from platform/auth. An admin of one workspace
// reading another's failures would be a cross-tenant read that nothing
// authorized — the singleton organization is a boot-time admission policy,
// not permission to stop scoping.
//
// The untenanted arm is CLOSED against the caller's declared dispatcher
// kinds rather than admitting every null. A dispatcher does no tenant work
// and its health is what an admin needs — a dispatcher that is not running
// means no workspace is being swept at all — but "the workspace key is
// null" is a property held by source-shape tests, not by a database
// constraint, and this table has no RLS while the app role holds direct
// CRUD on it. A malformed or externally inserted tenant row would otherwise
// land in a global arm and carry its kind, counts and failure class to
// every workspace's admin. An unrecognised untenanted row is omitted
// instead.
//
// $1 is bound as TEXT: ->> yields text, and binding a uuid-typed value
// gives pgx the uuid OID and Postgres "operator does not exist: text =
// uuid".
const healthScope = `(
	args->>'workspace_id' = $1
	OR (args->>'workspace_id' IS NULL AND kind = ANY($2))
)`

// KindHealth is one kind's live state under the caller's scope.
type KindHealth struct {
	Kind      string
	Queue     string
	FleetWide bool
	Waiting   int64
	Running   int64
	Retrying  int64
	// Dead is discarded plus cancelled: work that will not happen without
	// intervention. The two are reported together here because the question
	// the field answers is "will this run" and the answer is no either way;
	// the exposition endpoint keeps them apart, where the question is why.
	Dead int64
	// OldestWaitingAgeSeconds is nil when nothing of this kind is runnable
	// right now. Nil and zero are different claims — nothing waiting versus
	// something that just became runnable — so the absence is carried
	// rather than flattened.
	OldestWaitingAgeSeconds *float64
}

// Failure is one recent failed or failing job.
type Failure struct {
	// ID is river_job.id, and it is the ONE key that makes the rest of this
	// row actionable. River's own log lines carry job_id=<id>, and every
	// psql question an operator then asks — what were the args, what does
	// the full errors array say, is it still retrying — is keyed by it. A
	// failure list that pointed at the process log while naming no id was
	// pointing at a log nobody could search.
	ID   int64
	Kind string
	// WorkspaceID is nil for a dispatcher.
	WorkspaceID *string
	State       string
	Attempt     int
	MaxAttempts int
	FailedAt    time.Time
	// FirstFailedAt is when the FIRST recorded attempt failed, and it is nil
	// when no attempt error was recorded at all — a cancelled job that never
	// ran records none, and a zero time would read on a screen as 1970
	// rather than as absence. The absence is carried for the same reason
	// OldestWaitingAgeSeconds carries its own: nil and a value are different
	// claims, and flattening one into the other invents a fact.
	//
	// It answers a question the attempt counter cannot: "failing since
	// 21:08" and "failed once at 21:08" are different operator situations,
	// and an attempt count of 3 says which ladder rung the job is on, never
	// how long it has been on the ladder.
	FirstFailedAt *time.Time
	// StoredReason is river_job.errors' text VERBATIM, and the caller must
	// not put it on a wire without vetting it: the column holds whatever a
	// worker returned, and a worker that bypassed Fault stored a raw cause
	// that routinely names the address or record a provider refused. Ask
	// VettedFailure.
	StoredReason string
}

// Health is one scoped read of the job table for one workspace's admin.
type Health struct {
	Kinds    []KindHealth
	Failures []Failure
}

// recentFailureLimit bounds the failure list. It is a bounded view, not a
// log: an admin needs to see what is failing now, and an unbounded list
// over a table River retains for days is a different product.
const recentFailureLimit = 50

// WorkspaceHealth reads the job table for ONE workspace's admin: that
// workspace's own rows plus the untenanted rows of the named dispatcher
// kinds, and nothing else.
//
// dispatcherKinds is supplied by the caller rather than read off the
// declaration in this package. This table has no RLS, so the untenanted arm
// IS the scope — and a scope the calling surface states for itself is one
// that surface can be gated on, which is where the gate for it lives.
func WorkspaceHealth(ctx context.Context, pool *pgxpool.Pool, workspaceID string, dispatcherKinds []string) (Health, error) {
	kinds, err := healthByKind(ctx, pool, workspaceID, dispatcherKinds)
	if err != nil {
		return Health{}, err
	}
	failures, err := recentFailures(ctx, pool, workspaceID, dispatcherKinds)
	if err != nil {
		return Health{}, err
	}
	return Health{Kinds: kinds, Failures: failures}, nil
}

// healthByKind groups the caller's live rows by kind and queue.
//
// It groups by whether the workspace key is null as well, so a kind that
// somehow holds BOTH tenant and untenanted rows appears twice rather than
// being silently attributed to one side. That would be the workspace
// invariant breaking, and this endpoint should be the thing that shows it.
func healthByKind(ctx context.Context, pool *pgxpool.Pool, workspaceID string, dispatcherKinds []string) ([]KindHealth, error) {
	const q = `
		SELECT kind,
		       queue,
		       (args->>'workspace_id' IS NULL) AS fleet_wide,
		       count(*) FILTER (WHERE state::text IN ` + waitingStates + `)::bigint,
		       count(*) FILTER (WHERE state::text = 'running')::bigint,
		       count(*) FILTER (WHERE state::text = 'retryable')::bigint,
		       count(*) FILTER (WHERE state::text IN ` + terminalBadStates + `)::bigint,
		       max(EXTRACT(EPOCH FROM (now() - scheduled_at)))
		           FILTER (WHERE state::text IN ` + runnableStates + `
		                     AND scheduled_at <= now())::double precision
		FROM river_job
		WHERE state <> 'completed' AND ` + healthScope + `
		GROUP BY 1, 2, 3
		ORDER BY 1, 2, 3`

	rows, err := pool.Query(ctx, q, workspaceID, dispatcherKinds)
	if err != nil {
		return nil, fmt.Errorf("jobs: reading job health by kind: %w", err)
	}
	defer rows.Close()

	var out []KindHealth
	for rows.Next() {
		var k KindHealth
		if err := rows.Scan(&k.Kind, &k.Queue, &k.FleetWide,
			&k.Waiting, &k.Running, &k.Retrying, &k.Dead,
			&k.OldestWaitingAgeSeconds); err != nil {
			return nil, fmt.Errorf("jobs: scanning job health by kind: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: reading job health by kind: %w", err)
	}
	return out, nil
}

// recentFailures reads the most recent failed-or-failing rows under the
// caller's scope.
//
// River stores AttemptError{At, Attempt, Error, Trace} per element of a
// jsonb[]. Only the MESSAGE is selected, never the element: Trace carries a
// full panic stack, and serializing the object whole would carry it past
// the vetting the caller applies to the message alone. The index is
// 1-based and the newest attempt is last, so cardinality() is the newest —
// and it is null-safe in both directions, because errors is a nullable
// column and a cancelled row MAY carry no attempt error at all. River's
// cancel path is two paths, not one: a worker returning JobCancel(err)
// persists an AttemptError like any other failure, while a cancellation
// with no cause appends nothing, and the row is terminal either way.
//
// failed_at is read from COLUMNS, not from the attempt error's own
// timestamp: River sets AttemptError.At to the attempt's START (job_executor
// sets `At: e.start`), so a long-running job that failed after an hour would
// report the hour-ago moment it began and sort ahead of failures that
// actually happened later. finalized_at is the real failure moment for a
// terminal row, attempted_at is the closest available for a retryable one,
// and created_at covers a row that has never run — a total order over
// columns that always exist, with id breaking ties.
//
// Reading a column rather than the stored JSON also avoids casting
// app-written text in SQL:
// that column is app-written, and one malformed value would turn the whole
// endpoint into a 500 rather than one row into an approximation.
//
// first_failed_at is the one value that has no column to read, so errors[1]
// — the OLDEST attempt, the array being append-ordered — is read as TEXT and
// parsed in Go rather than cast in SQL. A `::timestamptz` in the statement
// would put the same app-written column back in the query's critical path
// that the paragraph above keeps out of it: one unparseable value would fail
// the whole read instead of leaving one row's "failing since" unanswered.
// errors is a jsonb[], not a jsonb, so it is subscripted 1-based; the
// jsonb path operators (errors->-1 and friends) do not apply to it at all
// and would not compile against this column.
func recentFailures(ctx context.Context, pool *pgxpool.Pool, workspaceID string, dispatcherKinds []string) ([]Failure, error) {
	q := `
		SELECT id,
		       kind,
		       args->>'workspace_id',
		       state::text,
		       attempt::int,
		       max_attempts::int,
		       coalesce(finalized_at, attempted_at, created_at),
		       errors[1]->>'at',
		       errors[cardinality(errors)]->>'error'
		FROM river_job
		WHERE state::text IN ('retryable','discarded','cancelled')
		  AND ` + healthScope + `
		ORDER BY coalesce(finalized_at, attempted_at, created_at) DESC, id DESC
		LIMIT ` + fmt.Sprint(recentFailureLimit)

	rows, err := pool.Query(ctx, q, workspaceID, dispatcherKinds)
	if err != nil {
		return nil, fmt.Errorf("jobs: reading recent job failures: %w", err)
	}
	defer rows.Close()

	var out []Failure
	for rows.Next() {
		var (
			f          Failure
			fallbackAt time.Time
			firstAt    *string
			stored     *string
		)
		if err := rows.Scan(&f.ID, &f.Kind, &f.WorkspaceID, &f.State, &f.Attempt,
			&f.MaxAttempts, &fallbackAt, &firstAt, &stored); err != nil {
			return nil, fmt.Errorf("jobs: scanning recent job failures: %w", err)
		}
		// UTC because pgx returns a timestamptz in the session's zone, and
		// two offsets inside one array is needlessly hostile to whoever
		// reads the list.
		f.FailedAt = fallbackAt.UTC()
		f.FirstFailedAt = parseFirstFailedAt(ctx, f.Kind, firstAt)
		if stored != nil {
			f.StoredReason = *stored
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: reading recent job failures: %w", err)
	}

	// Sorted on the same expression the SELECT ordered by, so the visible
	// failed_at descends exactly as the field name promises. The database
	// already returns them in this order; re-sorting keeps that true if the
	// two ever drift apart.
	slices.SortStableFunc(out, func(a, b Failure) int { return b.FailedAt.Compare(a.FailedAt) })
	return out, nil
}

// parseFirstFailedAt reads the oldest attempt error's own timestamp, and
// answers nil for every row that has none to read.
//
// Two absences arrive here and both are honest: a row with an empty errors
// array (River's causeless cancel path appends nothing) and a row whose
// element carries no `at` key. Neither is a failure of this read, and
// neither may become a zero time — the field is a pointer precisely so
// absence stays absence instead of rendering as 1970 in a failure list.
//
// A value that is PRESENT and unparseable is a third thing, and it is
// logged rather than dropped in silence. River writes RFC 3339 here, so
// text this cannot parse means something other than River wrote the row —
// worth an operator knowing about, and not worth failing the whole read
// for, because the rest of the row still tells them what died.
func parseFirstFailedAt(ctx context.Context, kind string, raw *string) *time.Time {
	if raw == nil {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, *raw)
	if err != nil {
		slog.WarnContext(ctx, "jobs: a job failure carries an unparseable first-attempt timestamp",
			"kind", kind, "err", err)
		return nil
	}
	utc := at.UTC()
	return &utc
}
