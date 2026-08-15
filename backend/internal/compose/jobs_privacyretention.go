// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// River wiring for the GDPR retention pass (data-model §3.4, ADR-0011): a
// dispatcher over EVERY workspace and a worker that evaluates one. It
// registers itself — workers and periodic schedule together — so jobs.go,
// which owns the runner's assembly, grows one line as this surface does.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	// privacyRetentionQueue keeps the retention pass off the default queue. One
	// workspace's pass applies up to a full batch per policy, each record in its
	// own multi-statement audited transaction, so a fanned-out fleet landing on
	// default would hold its five workers for minutes at a time and stall the
	// short maintenance jobs beside them.
	privacyRetentionQueue = "privacy_retention"
	// Two workers. A pass is bound by the database it already shares with every
	// other job on this process, not by anything outbound, so more workers buy
	// contention on the same pool rather than throughput — while two still keeps
	// one tenant's years of backlog from being the whole fleet's retention
	// latency.
	privacyRetentionMaxWorkers = 2
)

// privacyRetentionPassTimeout is the arithmetic behind
// privacy_retention_workspace's declared timeout, the heaviest of the
// fanned-out passes: privacy.MaxPassDuration is the pass's own ceiling (its
// stage count times its batch bound times its per-record allowance), and the
// margin covers the policy read and the per-stage due-list reads between
// records, which the pool bounds rather than this cap. Anything shorter cancels
// a tenant with a real backlog mid-record on every attempt until the row
// discards, leaving a permanently failing job row on the one obligation whose
// entire point is auditability.
//
// api/jobs.yaml carries the value River is actually handed, so moving this
// number alone moves no wall clock; the declaration names this constant in its
// derived timeout and the job census keeps the two equal.
//
// A var rather than a const because the engine derives its stage count from its
// own selector table instead of hand-counting it.
var privacyRetentionPassTimeout = privacy.MaxPassDuration + 5*time.Minute

// PrivacyRetentionConfig is the retention pass's slice of the runner's boot
// configuration.
type PrivacyRetentionConfig struct {
	// Interval is the dispatcher's cadence — the operator-facing
	// --retention-interval, which stays the schedule source it always was.
	// Non-positive schedules no retention dispatch; api/jobs.yaml declares it.
	Interval time.Duration
}

// addPrivacyRetentionJobs registers the retention workers and returns the
// dispatcher's periodic schedule for the caller to append.
//
// The service is built HERE rather than handed in because two of its
// dependencies come from opposite sides of the tree: the object store is the
// runner's own (Art. 17 reaches the attachment bytes), and the edge invalidator
// is a search closure, which a module may not import from privacy.
//
// A non-positive interval registers the workers but no schedule — the posture
// the declaration states and jobschedule.go resolves. It cannot reach a
// deployment that owes the storage-limitation obligation: --retention-interval
// carries a positive default and cmd/worker's validateSchedulerIntervals
// refuses a non-positive one at boot. The omission serves the callers that
// wire a runner for a few named passes and never meant to run this one.
func addPrivacyRetentionJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig, log *slog.Logger) []*river.PeriodicJob {
	// Retention removes the interactions the relationship graph is folded
	// from, so it carries the fold with it — in the same transaction, not on
	// the bus. Without the blobstore its erase action leaves the attachment
	// objects behind; without the invalidator the aggregates keep counting
	// interactions that no longer exist.
	// Built per job rather than once: this pass is fleet-wide, so the
	// workspace is the one the job names, not the one an installation
	// resolver would find.
	retention := func(db *database.DB) *privacy.RetentionService {
		return privacy.NewRetentionService(db, cfg.Blobstore, log).
			WithEdgeInvalidator(func(ctx context.Context, tx pgx.Tx, activityID ids.UUID) error {
				return search.RecomputeEdgesForActivities(ctx, tx, []ids.UUID{activityID})
			})
	}
	addDeclaredWorker[PrivacyRetentionArgs](reg, &privacyRetentionWorker{
		pool: pool, retention: retention, identity: identity.NewService(pool),
	})
	return periodicFor(cfg, PrivacyRetentionArgs{})
}

// PrivacyRetentionArgs evaluates the retention policies.
//
// It used to be a dispatcher that enumerated every workspace and enqueued one
// child per tenant. Under one installation that enumeration reads a single row,
// so the pair collapsed into this (ADR-0091 §5, ADR-0103 §1).
type PrivacyRetentionArgs struct{}

// Kind is the stable job identifier River persists in river_job. It is the
// DISPATCHER's old kind, deliberately: it is the name operators alert on.
func (PrivacyRetentionArgs) Kind() string { return "privacy_retention" }

// InsertOpts carries the attempt cap the declaration publishes, because the
// periodic insert supplies uniqueness and no attempt policy of its own. Held
// equal to api/jobs.yaml by TestArgsOwnedAttemptCapsMatchTheirDeclaration.
func (PrivacyRetentionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       privacyRetentionQueue,
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByState: activeSweepStates},
	}
}

// privacyRetentionWorker evaluates the policies and acts on what is due.
type privacyRetentionWorker struct {
	pool      *pgxpool.Pool
	retention func(*database.DB) *privacy.RetentionService
	identity  *identity.Service
}

func (w *privacyRetentionWorker) Work(ctx context.Context, _ *river.Job[PrivacyRetentionArgs]) error {
	// The installation is bound because the erasure and scrub writes this pass
	// drives still stamp a workspace from the context (storekit.MustWorkspace);
	// it retires with that helper (ADR-0091 §5).
	passCtx, err := installationJobCtx(ctx, w.identity)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	return jobs.FaultContext(ctx, w.retention(InstallationDB(w.pool)).EvaluateInstallation(retentionPassProvenance(passCtx)))
}

// retentionPassProvenance names who acted and under which pass. The engine
// writes an audit row and an outbox event per record it retires, so without
// this those rows would carry no actor and no correlation id — never that the
// machine moved it on a schedule, which is the whole answer a retention audit
// is read for.
func retentionPassProvenance(ctx context.Context) context.Context {
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}
