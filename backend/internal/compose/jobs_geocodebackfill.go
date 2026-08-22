// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The geocoding backfill: the pass that reaches companies an address write
// never will.
//
// Geocoding fires when an address is WRITTEN, which is the right trigger and a
// complete answer only for a company written after this installation had a
// geocoder. A seeded workspace, an import that ran last month, or an operator
// setting MARGINCE_GEOCODE_BASE_URL on a system that already holds its
// customers all leave rows nothing will ever write again — invisible to the
// trigger, and so never located. `within_radius` then answers from an empty
// set while looking exactly like a query that works.
//
// It NOMINATES, it does not decide. Each candidate goes to the same
// geocode_organization job an address write queues, and that worker re-asks
// AddressForGeocode — so the retry ledger, the settled-address rule and the
// attempt cap stay in one place rather than being restated here.

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// GeocodeBackfillArgs is one pass over the companies never asked about.
//
// It carries NO workspace, like WebhookRetryArgs and for the same reason: a
// periodic insert has no tenant to name, and a WorkspaceScoped kind whose args
// carry none fails every tick with "declares WorkspaceScoped but carries no
// workspace". The tenant belongs on the per-company job this pass queues —
// that is the one that reads and writes a row.
type GeocodeBackfillArgs struct{}

// Kind is the stable job identifier River persists in river_job.
func (GeocodeBackfillArgs) Kind() string { return "geocode_backfill" }

// InsertOpts carries the attempt cap the declaration publishes, because the
// periodic insert supplies uniqueness and no attempt policy of its own — the
// same reason webhook_retry states one.
func (GeocodeBackfillArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue:       geocodeQueue,
		MaxAttempts: 3,
		UniqueOpts:  river.UniqueOpts{ByState: activeSweepStates},
	}
}

// addGeocodeBackfillJobs registers the sweep and returns its schedule.
//
// A deployment with no geocoder registers NOTHING: there is nothing for the
// nominated jobs to be worked by, and queueing rows nobody can answer is worse
// than leaving the addresses unlocated, which is the honest state.
func addGeocodeBackfillJobs(reg *jobRegistry, pool *pgxpool.Pool, cfg JobRunnerConfig) []*river.PeriodicJob {
	if cfg.Geocoder == nil {
		return nil
	}
	addDeclaredWorker[GeocodeBackfillArgs](reg, &geocodeBackfillWorker{pool: pool})
	return periodicFor(cfg, GeocodeBackfillArgs{})
}

// geocodeBackfillWorker enqueues one batch of never-asked companies.
type geocodeBackfillWorker struct{ pool *pgxpool.Pool }

// Work nominates up to GeocodeBackfillBatch companies.
//
// One transaction for the whole batch, so a pass either queues its nominations
// or queues none: a partial batch would be re-read identically on the next
// tick anyway, and the deduplication on geocode_organization makes a repeat
// nomination harmless — but a half-committed pass that logged success would
// misreport what it did.
func (w *geocodeBackfillWorker) Work(ctx context.Context, _ *river.Job[GeocodeBackfillArgs]) error {
	workspaces, err := enumerateWorkspaces(ctx, w.pool)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	queued := 0
	for _, ws := range workspaces {
		n, err := w.sweepOneWorkspace(ctx, ws)
		if err != nil {
			return jobs.FaultContext(ctx, err)
		}
		queued += n
	}
	if queued == 0 {
		return nil
	}
	// Said out loud because the pace is the surprising part: the provider's
	// terms hold this installation to four lookups a minute, so a batch of
	// fifty is twelve minutes of work, and an operator watching for
	// coordinates should know that is normal rather than stuck.
	slog.InfoContext(ctx, "geocoding backfill queued a batch",
		"companies", queued, "batch", people.GeocodeBackfillBatch)
	return nil
}

// geocodeBackfillPriority is one rung below River's default, which is what an
// address write takes by saying nothing.
const geocodeBackfillPriority = river.PriorityDefault + 1

// geocodeBackfillOpts is geocodeInsertOpts with the sweep's own PRIORITY.
//
// Everything else is shared, so a nomination and an address write dedupe
// against each other rather than queueing the same company twice.
//
// The priority is what makes the sweep safe to run at all. One worker drains
// this queue at four lookups a minute, so a batch of fifty is twelve minutes
// of it — and without this a rep who corrects an address waits behind every
// one of them for a lookup they are watching for. River fetches every
// priority-1 job before any priority-2 job, so an address write always jumps
// the backlog. The reverse cannot starve: the sweep nominates rows nobody is
// touching, and rows nobody is touching produce no priority-1 work.
func geocodeBackfillOpts() *river.InsertOpts {
	opts := geocodeInsertOpts()
	opts.Priority = geocodeBackfillPriority
	return opts
}

// sweepOneWorkspace nominates one tenant's never-asked companies.
//
// The workspace is bound here rather than by the args, so each per-company job
// is stamped with the tenant whose row it will read — the binding the pass
// itself does not need and the lookup cannot do without.
func (w *geocodeBackfillWorker) sweepOneWorkspace(ctx context.Context, ws ids.UUID) (int, error) {
	// Reads under an actor of its own. Nothing queued this on a person's
	// behalf — it is the installation asking which of its own companies it
	// never located — so it names itself rather than borrowing a principal,
	// and organization:read is gated like any other read.
	wsCtx := geocodeBackfillActor(principal.WithWorkspaceID(ctx, ws))
	store := people.NewStore(database.Bind(w.pool, func(context.Context) (ids.WorkspaceID, error) {
		return ids.From[ids.WorkspaceKind](ws), nil
	}))
	due, err := store.ListGeocodeOrphans(wsCtx, people.GeocodeBackfillBatch)
	if err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}
	client, err := river.ClientFromContextSafely[pgx.Tx](wsCtx)
	if err != nil {
		return 0, err
	}
	for _, orgID := range due {
		if _, err := client.Insert(wsCtx, GeocodeOrganizationArgs{
			Workspace:      ws,
			OrganizationID: orgID.UUID,
		}, geocodeBackfillOpts()); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}

// geocodeBackfillActor binds the principal the sweep reads as, the same shape
// providerJobActor gives a provider run: a system principal that names what it
// is, so an audit row says the installation went looking rather than leaving a
// gated read with no one behind it.
func geocodeBackfillActor(ctx context.Context) context.Context {
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:geocode-backfill",
	})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}

// GeocodingConfig is the backfill's cadence.
//
// Separate from the Geocoder itself because they answer to different roles: the
// provider is what this installation may call, and the interval is how often it
// goes looking for work. A zero interval registers the worker and no schedule —
// the posture the declaration states — so an operator can turn the sweep off
// without giving up geocoding on write.
type GeocodingConfig struct {
	// BackfillInterval is how often the sweep looks for companies never asked
	// about. It runs on start too, so configuring a geocoder on a database that
	// already holds its customers begins locating them at boot rather than at
	// the first tick.
	BackfillInterval time.Duration
}
