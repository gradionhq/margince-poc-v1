// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The sweep's re-projection phase, proved against a real Postgres: a mirror
// row an OLDER mapping declaration projected is re-fetched, a row already at
// today's declaration is left alone, and once the re-fetch has landed the row
// leaves the stale set for good. Without this phase nothing clears a stale
// projection — the sweep is watermark-driven, and a record the incumbent has
// not touched is never re-read — so the flip preflight's block on those rows
// would be permanent rather than temporary.
//
// The assertions are on the ENQUEUED job, not on a completed re-fetch: what
// the job then does is overlay_refetch's own tested behaviour, and asserting
// it here would make an unrelated change to that worker fail this suite for
// the wrong reason. The one place this suite does run the worker is the
// convergence check, where the point is precisely that the two fit together.

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// staleDeclarationFingerprint stands in for a declaration that has since
// changed: it is not a fingerprint hubspot.ProjectionFingerprints answers
// today, which is the whole condition the phase selects on.
const staleDeclarationFingerprint = "a-declaration-that-has-since-changed"

// errUnexpectedJobKind is what the recorder answers when the phase hands it
// anything other than a re-fetch: an unchecked assertion would let a wrong job
// kind read as an enqueue of the right one.
var errUnexpectedJobKind = errors.New("compose: the re-projection phase enqueued something other than an overlay_refetch")

// reprojectionSweep is the sweep's incumbent AND its job queue, in one value,
// because the property under test spans both: which records the phases read,
// in what order, and what the last phase enqueued once they were done.
//
// Its Get re-stamps the record with today's declaration fingerprint while
// Backfill/Modified hand back whatever was seeded. That is the timeline the
// phase exists for — an estate mirrored under an older declaration, re-read
// after the declaration changed — and the fake cannot express it any other
// way, since it projects through no mapping of its own.
type reprojectionSweep struct {
	*fake.Adapter
	currentFingerprint string
	// phases records each incumbent-driven phase as it runs, and
	// phasesAtEnqueue snapshots that list at the moment a re-fetch was
	// enqueued — which is how the ordering claim ("re-projection runs after
	// the watermark phases") is checked rather than assumed.
	phases          []string
	phasesAtEnqueue []string
	enqueued        []OverlayRefetchArgs
}

func (r *reprojectionSweep) Backfill(ctx context.Context, objectClass, cursor string) (overlay.Page, error) {
	r.phases = append(r.phases, "backfill")
	return r.Adapter.Backfill(ctx, objectClass, cursor)
}

func (r *reprojectionSweep) Modified(ctx context.Context, objectClass string, since time.Time, cursor string) (overlay.Page, error) {
	r.phases = append(r.phases, "modified")
	return r.Adapter.Modified(ctx, objectClass, since, cursor)
}

func (r *reprojectionSweep) Deletions(ctx context.Context, objectClass string, since time.Time, cursor string) (overlay.DeletionPage, error) {
	r.phases = append(r.phases, "deletions")
	return r.Adapter.Deletions(ctx, objectClass, since, cursor)
}

func (r *reprojectionSweep) Get(ctx context.Context, objectClass, externalID string) (overlay.Record, error) {
	rec, err := r.Adapter.Get(ctx, objectClass, externalID)
	if err != nil {
		return overlay.Record{}, err
	}
	rec.ProjectionFingerprint = r.currentFingerprint
	return rec, nil
}

// Enqueue satisfies refetchEnqueuer, recording what the phase scheduled
// instead of inserting it — the sweep under test runs outside a River job, and
// the claim being checked is which rows it named, not River's insert.
func (r *reprojectionSweep) Enqueue(_ context.Context, args river.JobArgs, _ *river.InsertOpts) error {
	refetch, ok := args.(OverlayRefetchArgs)
	if !ok {
		return errUnexpectedJobKind
	}
	r.phasesAtEnqueue = slices.Clone(r.phases)
	r.enqueued = append(r.enqueued, refetch)
	return nil
}

// reprojectionEnv is one connected overlay workspace with its mirror store,
// its recording incumbent, and the contexts the sweep and the re-fetch worker
// each run under.
type reprojectionEnv struct {
	env      *integration.Env
	vault    keyvault.Vault
	ms       *overlay.MirrorStore
	inc      *reprojectionSweep
	due      overlay.DueOverlayConnection
	sweepCtx context.Context
	meter    *overlaybudget.Meter
}

// setupReprojection connects a workspace and seeds two contacts records: one
// carrying a declaration fingerprint that is no longer current, one already
// carrying today's. Both are mirrored by the first sweep's backfill through
// the real ingest, so the rows under test are written the way production
// writes them rather than hand-inserted.
func setupReprojection(t *testing.T) *reprojectionEnv {
	t.Helper()
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.DB(), unresolvedOwnerEmails{})
	adminCtx := overlayAdminCtx(e.WS, e.Rep1)
	if _, err := overlay.NewService(e.DB(), vault, ms).
		Connect(adminCtx, overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	current, ok := OverlayProjectionFingerprints()[overlay.IncumbentClassContacts]
	if !ok || current == "" {
		t.Fatal("the contacts declaration has no current fingerprint; every assertion below would be vacuous")
	}
	inc := &reprojectionSweep{Adapter: fake.New(), currentFingerprint: current}
	inc.SeedOwner("owner-1", "a@authz.test")
	// The owner map every live sweep seeds from the incumbent's directory
	// (reconcileConnection does it before the object classes): without it the
	// mirrored rows are readable by no one, and this suite's reads of them
	// would fail for a reason that has nothing to do with re-projection.
	// owner-1's directory email is Rep1's in the shared harness.
	if err := ms.WithResolver(inc).SeedUserMap(adminCtx, incumbentHubSpot,
		[]overlay.OwnerRef{{ExternalID: "owner-1", Email: "a@authz.test"}}); err != nil {
		t.Fatalf("SeedUserMap: %v", err)
	}
	// Modified well before the connection: the incremental phase's floor is
	// connected_at, so neither record is re-read by the watermark phases and
	// the re-projection phase is the only thing that can converge them —
	// which is exactly the production condition (a record the incumbent has
	// not touched since the mapping changed).
	for fingerprint, externalID := range map[string]string{
		staleDeclarationFingerprint: "c-old-declaration",
		current:                     "c-current-declaration",
	} {
		rec := fake.Rec(externalID, map[string]any{"firstname": "Ada"})
		rec.ObjectClass, rec.OwnerExternalID = "person", "owner-1"
		rec.ModifiedAt = time.Now().Add(-24 * time.Hour)
		rec.ProjectionFingerprint = fingerprint
		inc.Seed(overlay.IncumbentClassContacts, rec)
	}

	due := dueConnectionFor(adminCtx, t, e.Pool, e.WS)
	return &reprojectionEnv{
		env: e, vault: vault, ms: ms, inc: inc, due: due,
		sweepCtx: reconcileWorkerCtx(context.Background(), ids.From[ids.WorkspaceKind](e.WS)),
		meter:    workerBudgetMeter(t),
	}
}

// sweep runs one full sweep of incumbentClass — every phase, in order — the way
// reconcileConnection composes it for a live connection, recording what the
// re-projection phase scheduled instead of inserting it.
func (r *reprojectionEnv) sweep(t *testing.T, incumbentClass string) {
	t.Helper()
	r.sweepWith(t, incumbentClass, r.inc)
}

// sweepWith is sweep with the re-fetch enqueuer named, so a pass can be driven
// through the real insert surface (*jobs.Runner) instead of the recorder.
func (r *reprojectionEnv) sweepWith(t *testing.T, incumbentClass string, enqueue refetchEnqueuer) {
	t.Helper()
	deps := sweepDeps{
		inc:     r.inc,
		ms:      r.ms.WithResolver(r.inc).WithFenceIdentity(r.due.ConnectedAt),
		meter:   r.meter,
		enqueue: enqueue,
		log:     slog.New(slog.DiscardHandler),
	}
	if err := sweepObjectClass(r.sweepCtx, deps, r.due.Workspace, incumbentClass, r.due.ConnectedAt); err != nil {
		t.Fatalf("sweepObjectClass(%s): %v", incumbentClass, err)
	}
}

// seedMirroredActivity mirrors ONE engagement record through the real ingest:
// the fake serves it under its own incumbent class, the sweep's backfill writes
// it, and it lands in the shared canonical "activity" bucket under the mirror
// id a real adapter mints for that class — "<class>:<id>", OVA-MAP-7. Every
// class is given the SAME numeric id, which is what HubSpot's per-type id space
// permits and what the namespace exists to keep apart. fingerprint is the
// declaration the row records as its producer. It answers the mirror id, which
// is what a re-fetch of the row names.
func (r *reprojectionEnv) seedMirroredActivity(t *testing.T, incumbentClass, fingerprint string) string {
	t.Helper()
	externalID := incumbentClass + ":123"
	rec := fake.Rec(externalID, map[string]any{"subject": "Kickoff"})
	rec.ObjectClass, rec.OwnerExternalID = "activity", "owner-1"
	// Modified before the connection, for the reason setupReprojection states:
	// the watermark phases must not re-read it, so re-projection is the only
	// thing that can converge it.
	rec.ModifiedAt = time.Now().Add(-24 * time.Hour)
	rec.ProjectionFingerprint = fingerprint
	r.inc.Seed(incumbentClass, rec)
	r.sweep(t, incumbentClass)
	return externalID
}

func TestSweepReprojectionEnqueuesOnlyTheRowsAnOlderDeclarationProjected(t *testing.T) {
	r := setupReprojection(t)
	r.sweep(t, overlay.IncumbentClassContacts)

	want := []OverlayRefetchArgs{{
		Workspace:      r.env.WS,
		IncumbentClass: overlay.IncumbentClassContacts,
		ExternalID:     "c-old-declaration",
	}}
	if !slices.Equal(r.inc.enqueued, want) {
		t.Fatalf("the sweep enqueued %v, want %v — a row already carrying today's declaration must cost no incumbent read, "+
			"and a row an older one projected must be re-fetched or the flip stays blocked on it forever", r.inc.enqueued, want)
	}
	// Ordering: the re-fetch was scheduled only after every watermark phase had
	// run, so re-projection spends what the incumbent budget has left rather
	// than what keeping the mirror fresh needs.
	wantPhases := []string{"backfill", "modified", "deletions"}
	if !slices.Equal(r.inc.phasesAtEnqueue, wantPhases) {
		t.Errorf("phases completed before the re-projection enqueue = %v, want %v", r.inc.phasesAtEnqueue, wantPhases)
	}
}

func TestSweepReprojectionConvergesOnceTheRefetchLands(t *testing.T) {
	r := setupReprojection(t)
	r.sweep(t, overlay.IncumbentClassContacts)
	if len(r.inc.enqueued) != 1 {
		t.Fatalf("the first sweep enqueued %d re-fetches, want 1 — nothing to converge otherwise", len(r.inc.enqueued))
	}

	// Work the job the phase enqueued, through the real worker: the point of
	// this test is that the two halves fit — the phase names a row, the
	// re-fetch re-projects it under the current declaration, and the row
	// leaves the stale set.
	worker := &overlayRefetchWorker{
		pool: r.env.Pool, vault: r.vault, ms: r.ms,
		meter:        budgettest.Meter(t, budgettest.SmallConfig("hubspot")),
		log:          slog.New(slog.DiscardHandler),
		newIncumbent: func(_, _ string) overlay.Incumbent { return r.inc },
	}
	if err := worker.Work(context.Background(), &river.Job[OverlayRefetchArgs]{Args: r.inc.enqueued[0]}); err != nil {
		t.Fatalf("refetch Work: %v", err)
	}
	row, err := r.ms.Get(overlayReaderCtx(r.env.WS, r.env.Rep1), "person", "c-old-declaration")
	if err != nil {
		t.Fatalf("reading the re-projected row: %v", err)
	}
	if row.ProjectionFingerprint != r.inc.currentFingerprint {
		t.Fatalf("the re-fetched row records %q, want the current declaration %q — the ingest guard admits a re-projection "+
			"at the same baseline, and without that nothing here converges", row.ProjectionFingerprint, r.inc.currentFingerprint)
	}

	r.inc.enqueued = nil
	r.sweep(t, overlay.IncumbentClassContacts)
	if len(r.inc.enqueued) != 0 {
		t.Fatalf("the second sweep enqueued %v, want none — a converged class must cost nothing every tick", r.inc.enqueued)
	}
}

// The five engagement classes share the canonical "activity" bucket, so a
// calls pass and a meetings pass select from the same rows — and a re-fetch
// names the INCUMBENT class, so a row handed to the wrong pass is a live read
// for a record that class does not hold. Attribution is by the mirror id's own
// namespace, and this is the case it exists for: two rows of one canonical
// class, projected by different declarations, carrying the same numeric
// incumbent id.
func TestSweepReprojectionAttributesActivityRowsByTheirMirrorNamespace(t *testing.T) {
	r := setupReprojection(t)
	callID := r.seedMirroredActivity(t, overlay.IncumbentClassCalls, staleDeclarationFingerprint)
	meetingID := r.seedMirroredActivity(t, overlay.IncumbentClassMeetings, staleDeclarationFingerprint)

	r.inc.enqueued = nil
	r.sweep(t, overlay.IncumbentClassCalls)

	want := []OverlayRefetchArgs{{
		Workspace:      r.env.WS,
		IncumbentClass: overlay.IncumbentClassCalls,
		ExternalID:     callID,
	}}
	if !slices.Equal(r.inc.enqueued, want) {
		t.Fatalf("the calls sweep enqueued %v, want %v — %q is just as stale, but only the calls endpoint can serve %q, "+
			"and re-reading a meeting under /calls can only 404", r.inc.enqueued, want, meetingID, callID)
	}
}

// The Critical case attribution has to survive: renaming a declaration's
// constant is an ordinary registry edit, and it changes the declaration's
// fingerprint — so every row the old declaration projected is stale at once and
// the flip blocks on them. Attributing those rows by anything the declaration
// writes INTO the payload would select none of them in exactly that pass, and
// the block would never clear. The mirror id's namespace is not the
// declaration's to change, so the sweep still names them.
func TestStaleProjectionsSurviveADeclarationConstantChange(t *testing.T) {
	r := setupReprojection(t)
	callID := r.seedMirroredActivity(t, overlay.IncumbentClassCalls, currentFingerprintFor(t, overlay.IncumbentClassCalls))
	meetingID := r.seedMirroredActivity(t, overlay.IncumbentClassMeetings, currentFingerprintFor(t, overlay.IncumbentClassMeetings))

	m, ok := hubspot.Mapping(overlay.IncumbentClassCalls)
	if !ok {
		t.Fatalf("Mapping(%q): the registry declares no calls mapping", overlay.IncumbentClassCalls)
	}
	stale, err := r.ms.StaleProjections(r.sweepCtx, m, reprojectionEnqueueLimit)
	if err != nil {
		t.Fatalf("StaleProjections under today's declaration: %v", err)
	}
	if len(stale) != 0 {
		t.Fatalf("today's declaration reports %v stale — the rows it just projected must be current, "+
			"or the assertion below proves nothing", stale)
	}

	// The registry edit: the constant naming the activity kind is renamed.
	m.Const = map[string]any{"kind": "call-renamed"}
	stale, err = r.ms.StaleProjections(r.sweepCtx, m, reprojectionEnqueueLimit)
	if err != nil {
		t.Fatalf("StaleProjections under the renamed declaration: %v", err)
	}
	if !slices.Equal(stale, []string{callID}) {
		t.Fatalf("the renamed declaration reports %v stale, want [%s] — every row it projected went stale with the rename, "+
			"so a sweep that names none of them leaves the flip blocked forever, and one that names %q re-reads a meeting as a call",
			stale, callID, meetingID)
	}
}

// currentFingerprintFor answers the fingerprint today's declaration for
// incumbentClass stamps, failing loudly rather than letting a row seeded with an
// empty string read as "projected by the current mapping".
func currentFingerprintFor(t *testing.T, incumbentClass string) string {
	t.Helper()
	fingerprint, ok := OverlayProjectionFingerprints()[incumbentClass]
	if !ok || fingerprint == "" {
		t.Fatalf("the %s declaration has no current fingerprint", incumbentClass)
	}
	return fingerprint
}

// A sweep tick runs again long before an earlier re-fetch has been worked, so
// the phase re-selects a row it already named — every pass, until the re-fetch
// lands. What keeps that from stacking one live incumbent read per tick is
// River's own coalescing over reprojectionInsertOpts, which only a real insert
// exercises: this pass enqueues through *jobs.Runner, the same insert surface
// the webhook lane's receiver uses, and counts the rows it left behind.
func TestSweepReprojectionCoalescesRepeatedPassesIntoOneJob(t *testing.T) {
	r := setupReprojection(t)
	integration.ApplyRiverSchema(t)
	inserter, err := jobs.NewInserter(r.env.Pool, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewInserter: %v", err)
	}
	ctx := context.Background()
	for pass := 1; pass <= 2; pass++ {
		r.sweepWith(t, overlay.IncumbentClassContacts, inserter)
		if got := countJobsOfKind(ctx, t, r.env.Pool, OverlayRefetchArgs{}.Kind()); got != 1 {
			t.Fatalf("%d overlay_refetch rows after sweep pass %d, want exactly 1 — a pass that stacks a second job "+
				"for a row still queued spends one live incumbent read per tick on a record already waiting to be read", got, pass)
		}
	}
}
