// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The geocoding worker: one company's address becomes a point.
//
// It runs on its own queue at ONE worker, and that bound is the usage policy
// rather than a performance choice. Nominatim holds a client that runs on a
// schedule to four requests a minute, single-threaded, against one service —
// so a second worker would be a second requester however carefully each paced
// itself. The pacer in platform/geocode enforces the interval; the queue's
// max_workers enforces the single thread. Neither alone is enough.
//
// THE TRIGGER IS INGESTION, not a sweep. A company is geocoded when its
// address is written or changed, so the lookups arrive at the rate companies
// are read rather than in a daily burst — which is also why there is no
// backfill job and no daily budget to contend over. Re-reading a website is
// the backfill.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/geocode"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// GeocodeOrganizationArgs is one queued lookup: the tenant and the company.
//
// The ADDRESS IS NOT among them, deliberately. The worker reads it from the
// row when it runs, so a lookup queued before an edit resolves the address the
// company actually has rather than the one it had when the job was made. A
// copy in the args would be the stale-coordinate bug moved one layer out.
type GeocodeOrganizationArgs struct {
	Workspace      ids.UUID `json:"workspace_id"`
	OrganizationID ids.UUID `json:"organization_id"`
}

// Kind is the stable job identifier River persists in river_job.
func (GeocodeOrganizationArgs) Kind() string { return "geocode_organization" }

// WorkspaceID binds this lookup to its tenant (jobs.WorkspaceScoped).
func (a GeocodeOrganizationArgs) WorkspaceID() ids.UUID { return a.Workspace }

// GeocodeEnqueueFor builds the in-transaction enqueue the people store calls
// when an address is written.
//
// Nil-safe by contract, and nil is a real composition: a deployment with no
// geocoder writes the address and queues nothing. The address is what the
// caller asked for; the coordinates are what this installation can offer.
func GeocodeEnqueueFor(enqueue geocodeEnqueuer) people.GeocodeEnqueue {
	if enqueue == nil {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx, orgID ids.OrganizationID) error {
		ws, ok := principal.WorkspaceID(ctx)
		if !ok {
			// No tenant bound means no job that could ever be worked: River
			// scopes by workspace, and a row without one is a job nobody
			// dequeues. Refusing is louder than inserting an orphan.
			return errors.New("compose: geocoding a company outside any workspace")
		}
		return enqueue.EnqueueTx(ctx, tx, GeocodeOrganizationArgs{
			Workspace:      ws,
			OrganizationID: orgID.UUID,
		}, geocodeInsertOpts())
	}
}

// geocodeEnqueuer is the insert-only half of the job runner, same seam the
// transcript reading uses: the api role queues, the worker role works.
type geocodeEnqueuer interface {
	EnqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) error
}

// geocodeInsertOpts puts the job on its own queue and DEDUPLICATES by args.
//
// Deduplication matters more here than elsewhere: a rep correcting a typo in
// an address line by line would otherwise queue one lookup per keystroke-sized
// save, and each of those costs 15 seconds of a rate the whole installation
// shares. One pending lookup per company is all that is ever useful, because
// the worker reads the address when it RUNS rather than from the args.
func geocodeInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue: geocodeQueue,
		// The states matter as much as ByArgs. River's default duplicate set
		// includes running and completed, which is wrong here: a company edited
		// while its lookup is in flight would have its successor job silently
		// dropped, and the row would sit stale forever with nothing to resolve
		// it. Only a job still WAITING is a duplicate — one already running
		// resolved a different address, and one that completed is history.
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateScheduled},
		},
		// The provider is asked at most this many times for one address. River's
		// own default would keep retrying past the point the attempt ledger
		// stops caring, spending the installation's shared rate on a lookup
		// AddressForGeocode already refuses.
		MaxAttempts: geocodeMaxAttempts,
	}
}

// geocodeQueue is declared in api/jobs.yaml at one worker; the name is spelled
// here because the insert has to name it.
const (
	geocodeQueue = "geocode"
	// geocodeMaxWorkers mirrors api/jobs.yaml. One, for the policy reason
	// stated at the top of this file.
	geocodeMaxWorkers = 1
	// geocodeMaxAttempts mirrors people.geocodeMaxAttempts: River stops
	// retrying at the same count the attempt ledger stops accepting.
	geocodeMaxAttempts = 3
)

// geocodeWorker resolves one company's address.
//
// It is registered whether or not a geocoder is configured (registers_anyway
// in api/jobs.yaml), which is deliberate: a queued job on a deployment that
// later loses its provider must be answerable, and a worker that was never
// registered leaves it stuck rather than recorded.
type geocodeWorker struct {
	river.WorkerDefaults[GeocodeOrganizationArgs]
	pool     *pgxpool.Pool
	geocoder geocode.Client
}

// newGeocodeWorker assembles the worker-role lookup. geocoder may be nil — a
// deployment with no provider records the refusal rather than retrying.
// No logger: every failure this worker has is RETURNED, through
// jobs.FaultContext, so it lands in river_job.errors where an operator looking
// at a stuck job will actually find it. A log line beside a returned error is
// the same fact in a second place nobody correlates.
func newGeocodeWorker(pool *pgxpool.Pool, geocoder geocode.Client) *geocodeWorker {
	return &geocodeWorker{pool: pool, geocoder: geocoder}
}

// Work performs the lookup and records whatever came back.
//
// EVERY OUTCOME IS WRITTEN, including the ones with no coordinates. A company
// whose address resolves to nothing must be remembered as such or the next
// address write asks again forever; a lookup that failed must be remembered
// too, so the attempt ledger can stop retrying it. The only path that records
// nothing is the one where there was nothing to ask about.
func (w *geocodeWorker) Work(ctx context.Context, job *river.Job[GeocodeOrganizationArgs]) error {
	args := job.Args
	// Bound through the shared helper, so the args' own WorkspaceID() IS the
	// binding: a worker that picked its own could claim one workspace and work
	// in another. Under a NEW name rather than reassigning ctx, because
	// workspaceJobCtx returns a nil context alongside its error and the refusal
	// below has to report through a context that exists.
	wsCtx, err := workspaceJobCtx(ctx, args)
	if err != nil {
		return jobs.FaultContext(ctx, err)
	}
	store := people.NewStore(database.Bind(w.pool, func(context.Context) (ids.WorkspaceID, error) {
		return ids.From[ids.WorkspaceKind](args.Workspace), nil
	}))
	orgID := ids.From[ids.OrganizationKind](args.OrganizationID)

	address, ok, err := store.AddressForGeocode(wsCtx, orgID)
	if err != nil {
		return jobs.FaultContext(wsCtx, fmt.Errorf("reading the address to geocode: %w", err))
	}
	if !ok {
		// Nothing to ask about: no address, an address already resolved, or a
		// company whose attempts are spent. All three mean "do not ask", and
		// the worker does not need to tell them apart.
		return nil
	}
	if w.geocoder == nil {
		// A deployment with no geocoder should not have queued this. Recording
		// the refusal rather than failing keeps the job from retrying against a
		// provider that will never exist.
		return jobs.FaultContext(wsCtx,
			store.RecordGeocode(wsCtx, orgID, people.GeocodeFailed, nil, nil, "", address.InputHash))
	}

	// THE CACHE IS ASKED FIRST, and it is a policy requirement rather than an
	// optimisation: Nominatim's terms require a client that runs regularly to
	// cache. Several companies share one street or one small town, and each
	// repeat would otherwise spend fifteen seconds of a budget the whole
	// installation shares.
	if cached, hit, err := store.LookupPlace(wsCtx, address.Query); err != nil {
		return jobs.FaultContext(wsCtx, fmt.Errorf("reading the place cache: %w", err))
	} else if hit {
		return jobs.FaultContext(wsCtx, store.RecordGeocode(wsCtx, orgID, people.GeocodeOK,
			&cached.Lat, &cached.Lon, cached.Provider, address.InputHash))
	}

	point, found, err := w.geocoder.Resolve(wsCtx, address.Query)
	if err != nil {
		// The lookup did not complete. Recorded as failed so the ledger counts
		// the attempt, and returned so River retries — a rate limit or a network
		// fault is worth asking again, unlike an address that does not exist.
		//
		// The ledger write's own failure is JOINED rather than logged and
		// dropped: this job is failing either way, and a reader of
		// river_job.errors should see both causes rather than one plus a log
		// line nobody correlates.
		// A provider that named a wait gets that wait. Retrying on River's own
		// schedule when Nominatim has said "not for ten minutes" is how a rate
		// limit becomes a block.
		recErr := store.RecordGeocode(wsCtx, orgID, people.GeocodeFailed, nil, nil, "", address.InputHash)
		var refused *geocode.ProviderRefusedError
		if errors.As(err, &refused) && refused.RetryAfter > 0 {
			recErr = store.RecordGeocodeBackoff(wsCtx, orgID, address.InputHash, refused.RetryAfter)
		}
		return jobs.FaultContext(wsCtx, errors.Join(
			fmt.Errorf("geocoding %q: %w", address.Query, err), recErr))
	}
	if !found {
		// The geocoder answered, and the answer is that this address is not a
		// place. A FACT about the address, so it is recorded and NOT retried:
		// asking again changes nothing until the address itself changes.
		return jobs.FaultContext(wsCtx,
			store.RecordGeocode(wsCtx, orgID, people.GeocodeNoMatch, nil, nil, geocodeProvider, address.InputHash))
	}
	// Remembered BEFORE the row is written, so a failure to record the company
	// does not also lose the lookup: the point is a fact about a place, and the
	// next company on that street should not have to pay for it again.
	// The cache write's failure is JOINED rather than logged and dropped. It is
	// the less important of the two writes — the company still gets its point —
	// but a cache that silently stops taking entries makes this installation
	// re-ask the provider for every place, which is the policy breach the cache
	// exists to prevent. A reader of river_job.errors should see it.
	cacheErr := store.RememberPlace(wsCtx, address.Query, people.CachedPlace{
		Lat: point.Lat, Lon: point.Lon, Provider: geocodeProvider,
	})
	return jobs.FaultContext(wsCtx, errors.Join(
		store.RecordGeocode(wsCtx, orgID, people.GeocodeOK, &point.Lat, &point.Lon, geocodeProvider, address.InputHash),
		cacheErr))
}

// geocodeProvider names what answered, recorded on the row so a later change
// of provider is visible in the data rather than only in the config.
const geocodeProvider = "nominatim"

// WithGeocoding lets an address write queue a coordinate lookup.
//
// Without it a company's address still writes and nothing is geocoded, which
// is the honest posture for a deployment that has no geocoder: within_radius
// stays unavailable rather than answering from an empty table.
//
// It wires the ENQUEUE, not the provider. The provider lives on the worker
// role (JobRunnerConfig.Geocoder); this is the api role's half, and the two are
// deliberately separate — an installation can queue lookups from one process
// and perform them in another, which is what keeps the single-requester rule
// enforceable at one worker.
func WithGeocoding(inserter *jobs.Runner) Option {
	return func(s *Server, _ *pgxpool.Pool) {
		s.peopleStore = s.peopleStore.WithGeocodeEnqueue(GeocodeEnqueueFor(inserter))
	}
}
