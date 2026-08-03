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
	Kind string
	// WorkspaceID is nil for a dispatcher.
	WorkspaceID *string
	State       string
	Attempt     int
	MaxAttempts int
	FailedAt    time.Time
	// StoredReason is river_job.errors' text VERBATIM, and the caller must
	// not put it on a wire without vetting it: the column holds whatever a
	// worker returned, and a worker that bypassed Fault stored a raw cause
	// that routinely names the address or record a provider refused. Ask
	// VettedSentence.
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
// dispatcherKinds is passed in rather than derived here because which
// kinds are fleet-wide is a fact about the composition layer's job
// catalog, and platform owns no domain.
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
// column and River's cancel path appends nothing at all, so a cancelled row
// can be terminal with no attempt error to read.
//
// The order is finalized_at when the row is finalized and created_at when
// it is not, with id breaking ties — a total order over columns that always
// exist. The attempt error's own timestamp is deliberately NOT cast in SQL:
// that column is app-written, and one malformed value would turn the whole
// endpoint into a 500 rather than one row into an approximation.
func recentFailures(ctx context.Context, pool *pgxpool.Pool, workspaceID string, dispatcherKinds []string) ([]Failure, error) {
	q := `
		SELECT kind,
		       args->>'workspace_id',
		       state::text,
		       attempt::int,
		       max_attempts::int,
		       coalesce(finalized_at, created_at),
		       errors[cardinality(errors)]->>'at',
		       errors[cardinality(errors)]->>'error'
		FROM river_job
		WHERE state::text IN ('retryable','discarded','cancelled')
		  AND ` + healthScope + `
		ORDER BY coalesce(finalized_at, created_at) DESC, id DESC
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
			attemptAt  *string
			stored     *string
		)
		if err := rows.Scan(&f.Kind, &f.WorkspaceID, &f.State, &f.Attempt,
			&f.MaxAttempts, &fallbackAt, &attemptAt, &stored); err != nil {
			return nil, fmt.Errorf("jobs: scanning recent job failures: %w", err)
		}
		f.FailedAt = fallbackAt
		if attemptAt != nil {
			if at, err := time.Parse(time.RFC3339Nano, *attemptAt); err == nil {
				f.FailedAt = at
			}
		}
		if stored != nil {
			f.StoredReason = *stored
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobs: reading recent job failures: %w", err)
	}
	return out, nil
}
