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
// the wrong reason. The suite runs the worker only where the point is precisely
// that the two halves fit together: a re-fetch that lands and converges the
// row, and one that can never land and records why.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/database"
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

// The two contacts records setupReprojection mirrors, named by what produced
// them: one a declaration that has since changed, one today's.
const (
	staleRowExternalID   = "c-old-declaration"
	currentRowExternalID = "c-current-declaration"
)

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
		staleDeclarationFingerprint: staleRowExternalID,
		current:                     currentRowExternalID,
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
		ExternalID:     staleRowExternalID,
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

	// Work the job the phase enqueued, through the worker THIS build registers
	// (registeredRefetchWorker): the point of this test is that the two halves
	// fit in the wiring a deployed process runs — the phase names a row, the
	// re-fetch re-projects it under the current declaration, and the row leaves
	// the stale set. A worker assembled here instead would prove only that the
	// phase fits a worker this file built.
	worker := registeredRefetchWorker(t, r.env.Pool, r.vault,
		budgettest.Meter(t, budgettest.SmallConfig("hubspot")), r.inc)
	if err := worker.Work(context.Background(), &river.Job[OverlayRefetchArgs]{Args: r.inc.enqueued[0]}); err != nil {
		t.Fatalf("refetch Work: %v", err)
	}
	row, err := r.ms.Get(overlayReaderCtx(r.env.WS, r.env.Rep1), "person", staleRowExternalID)
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

// The hardest case for attribution: renaming a declaration's constant is an
// ordinary registry edit, and it changes the declaration's fingerprint — so
// every row the old declaration projected is stale at once and the flip blocks
// on them. Attributing those rows by anything the declaration
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

// mirrorContactsAndDeclaration runs the sweep that mirrors setupReprojection's
// two contacts records through the real ingest, and answers the declaration
// they are judged against — read from the registry, so the fingerprint the
// assertions turn on is the one this build actually stamps. That pair is the
// starting state every case below shares.
func (r *reprojectionEnv) mirrorContactsAndDeclaration(t *testing.T) overlay.ObjectMapping {
	t.Helper()
	r.sweep(t, overlay.IncumbentClassContacts)
	m, ok := hubspot.Mapping(overlay.IncumbentClassContacts)
	if !ok {
		t.Fatalf("Mapping(%q): the registry declares no contacts mapping", overlay.IncumbentClassContacts)
	}
	return m
}

// staleProjections is StaleProjections under the sweep's own principal, failing
// the test rather than returning an error nobody would act on.
func (r *reprojectionEnv) staleProjections(t *testing.T, m overlay.ObjectMapping) []string {
	t.Helper()
	stale, err := r.ms.StaleProjections(r.sweepCtx, m, reprojectionEnqueueLimit)
	if err != nil {
		t.Fatalf("StaleProjections(%s): %v", m.Source, err)
	}
	return stale
}

// A row that has already failed against the CURRENT declaration is not
// re-fetched: the incumbent serves the record whole and this declaration cannot
// project it, so the same read returns the same answer while the declaration
// stands, and every attempt spends one call from a budget interactive
// force-fresh shares.
func TestStaleProjectionsSkipARowThatFailedAgainstTheCurrentDeclaration(t *testing.T) {
	r := setupReprojection(t)
	m := r.mirrorContactsAndDeclaration(t)

	if stale := r.staleProjections(t, m); !slices.Equal(stale, []string{staleRowExternalID}) {
		t.Fatalf("before the failure is recorded StaleProjections reports %v, want [%s] — "+
			"the skip below would prove nothing about a row the sweep never named", stale, staleRowExternalID)
	}
	if err := r.ms.RecordReprojectionFailure(r.sweepCtx, m.Target, staleRowExternalID, overlay.Fingerprint(m)); err != nil {
		t.Fatalf("RecordReprojectionFailure: %v", err)
	}

	if stale := r.staleProjections(t, m); len(stale) != 0 {
		t.Fatalf("StaleProjections still reports %v after the row failed against this very declaration, want none — "+
			"re-reading it cannot change the answer, and the sweep would spend one incumbent call on it every tick, forever", stale)
	}
}

// The record names ONE declaration. A repaired declaration has a different
// fingerprint, so the record stops applying and the row is retried — the exit
// that fixes the data rather than discarding it, with no record for anyone to
// clear by hand.
func TestStaleProjectionsRetryARowWhoseDeclarationChanged(t *testing.T) {
	r := setupReprojection(t)
	m := r.mirrorContactsAndDeclaration(t)

	if err := r.ms.RecordReprojectionFailure(r.sweepCtx, m.Target, staleRowExternalID, overlay.Fingerprint(m)); err != nil {
		t.Fatalf("RecordReprojectionFailure: %v", err)
	}
	if stale := r.staleProjections(t, m); len(stale) != 0 {
		t.Fatalf("StaleProjections reports %v under the declaration the row failed against, want none — "+
			"the retry below would prove nothing if the row had never left the set", stale)
	}

	// The repair: a build that ships a changed declaration, which is the only
	// thing that can fix a record this build cannot project — the declaration
	// is Go source compiled into the binary. It re-fingerprints every row the
	// old declaration projected, the recorded failure among them.
	m.Const = map[string]any{"lifecycle_source": "repaired"}
	want := []string{currentRowExternalID, staleRowExternalID}
	if stale := r.staleProjections(t, m); !slices.Equal(stale, want) {
		t.Fatalf("the repaired declaration reports %v stale, want %v — a record that outlived the declaration it names "+
			"would strand the row against a mapping nobody can retry it under", stale, want)
	}
}

// errRecordThisBuildCannotProject is what the incumbent adapter answers for a
// record it served whole that this build's declaration could not project
// (hubspot.ErrUnmappable, as hubspot.mapRecord returns it). It is the ONE read
// failure whose answer is fixed for as long as the declaration is, which is
// what makes retiring the row from the sweep honest.
var errRecordThisBuildCannotProject = fmt.Errorf("%w: mapping contacts record %s", hubspot.ErrUnmappable, staleRowExternalID)

// errRecordWithheldThisPass stands for every OTHER object-level read failure:
// a batch read that came back with no results, a 409. Only the classification
// matters here — it carries no sentinel this worker records on — and that the
// real empty-batch error carries none is pinned where it is produced, by
// hubspot's TestAdapterGetDoesNotCallAnEmptyBatchResultUnmappable.
var errRecordWithheldThisPass = errors.New("hubspot: no contacts record with external id " + staleRowExternalID)

// reprojectionFailureRecord answers the declaration a row records it could not
// reach, read straight from the column. That is the direct evidence and the
// only kind there is here: a record written under the wrong object class
// updates zero rows, returns no error, and leaves the mirror looking exactly as
// it does when nothing was recorded at all.
func (r *reprojectionEnv) reprojectionFailureRecord(t *testing.T, canonicalClass, externalID string) string {
	t.Helper()
	var recorded *string
	if err := database.WithWorkspaceTx(r.sweepCtx, r.env.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(r.sweepCtx, `SELECT reprojection_failed_for FROM overlay_mirror
			WHERE object_class = $1 AND external_id = $2`, canonicalClass, externalID).Scan(&recorded)
	}); err != nil {
		t.Fatalf("reading %s/%s's re-projection failure record: %v", canonicalClass, externalID, err)
	}
	if recorded == nil {
		return ""
	}
	return *recorded
}

// The re-fetch the phase enqueues can fail for good: the incumbent serves the
// record whole and this build's declaration cannot project it, so the read
// returns the same answer every time until the declaration changes. The worker
// records the declaration it failed to reach, and that record is what takes the
// row out of the stale set — without it the sweep names the row again every
// tick, spending one reserved incumbent call from the budget interactive
// force-fresh shares, forever, while the flip stays blocked.
func TestSweepReprojectionRecordsARefetchThatCannotLand(t *testing.T) {
	r := setupReprojection(t)
	m := r.mirrorContactsAndDeclaration(t)
	want := []OverlayRefetchArgs{{
		Workspace:      r.env.WS,
		IncumbentClass: overlay.IncumbentClassContacts,
		ExternalID:     staleRowExternalID,
	}}
	if !slices.Equal(r.inc.enqueued, want) {
		t.Fatalf("the first sweep enqueued %v, want %v — there is no doomed re-fetch to work otherwise", r.inc.enqueued, want)
	}

	// The worker THIS build registers, pointed at an incumbent that will not
	// serve the record: the failure happens inside the worker under test rather
	// than being staged against the mirror by this file.
	worker := registeredRefetchWorker(t, r.env.Pool, r.vault,
		budgettest.Meter(t, budgettest.SmallConfig("hubspot")),
		getFailingIncumbent{Adapter: fake.New(), err: errRecordThisBuildCannotProject})
	if err := worker.Work(context.Background(), &river.Job[OverlayRefetchArgs]{Args: r.inc.enqueued[0]}); err != nil {
		t.Fatalf("a read no retry can change must not fail the job: %v", err)
	}

	// The mirror keys on the CANONICAL class a declaration projects onto
	// ("person"), never the incumbent's own name for it ("contacts"), and the
	// fingerprint is the one StaleProjections compares against.
	if recorded := r.reprojectionFailureRecord(t, m.Target, staleRowExternalID); recorded != overlay.Fingerprint(m) {
		t.Fatalf("%s/%s records %q, want %q — the declaration the re-fetch failed to reach, keyed the way the mirror is keyed; "+
			"a record the row never receives is silent, and the skip below is the only thing that would ever notice",
			m.Target, staleRowExternalID, recorded, overlay.Fingerprint(m))
	}

	r.inc.enqueued = nil
	r.sweep(t, overlay.IncumbentClassContacts)
	if len(r.inc.enqueued) != 0 {
		t.Fatalf("the second sweep enqueued %v, want none — re-reading a row that just failed against this very declaration "+
			"buys the same answer for one more live incumbent call", r.inc.enqueued)
	}
}

// The other side of the same decision, and the one that costs an estate its
// convergence if it goes wrong. Every read failure that is NOT the declaration
// refusing the record can come back differently on the next tick — an empty
// batch result is what HubSpot's partial-batch 207 produces for an object it
// momentarily withheld, and a 409 is a passing state conflict. Recording one of
// those retires the row from the sweep while it goes on counting stale for the
// flip: the flip would stay blocked with nothing left re-fetching the row, and
// the one-per-tick re-read that used to say so would be gone too.
func TestSweepReprojectionKeepsARowWhoseRecordWasMerelyWithheld(t *testing.T) {
	r := setupReprojection(t)
	m := r.mirrorContactsAndDeclaration(t)
	if !slices.Equal(r.inc.enqueued, []OverlayRefetchArgs{{
		Workspace:      r.env.WS,
		IncumbentClass: overlay.IncumbentClassContacts,
		ExternalID:     staleRowExternalID,
	}}) {
		t.Fatalf("the first sweep enqueued %v, want the stale row alone — there is no re-fetch to fail otherwise", r.inc.enqueued)
	}

	worker := registeredRefetchWorker(t, r.env.Pool, r.vault,
		budgettest.Meter(t, budgettest.SmallConfig("hubspot")),
		getFailingIncumbent{Adapter: fake.New(), err: errRecordWithheldThisPass})
	if err := worker.Work(context.Background(), &river.Job[OverlayRefetchArgs]{Args: r.inc.enqueued[0]}); err != nil {
		t.Fatalf("a read the next pass can retry must still not fail the job: %v", err)
	}

	if recorded := r.reprojectionFailureRecord(t, m.Target, staleRowExternalID); recorded != "" {
		t.Fatalf("%s/%s records %q, want nothing — the incumbent answered this pass, not this declaration, and a record here "+
			"retires the row from the sweep permanently while the flip goes on counting it stale",
			m.Target, staleRowExternalID, recorded)
	}

	r.inc.enqueued = nil
	r.sweep(t, overlay.IncumbentClassContacts)
	if !slices.Equal(r.inc.enqueued, []OverlayRefetchArgs{{
		Workspace:      r.env.WS,
		IncumbentClass: overlay.IncumbentClassContacts,
		ExternalID:     staleRowExternalID,
	}}) {
		t.Fatalf("the next sweep enqueued %v, want the stale row again — a row the incumbent withheld once is the sweep's "+
			"to retry, and dropping it strands the flip on a row nothing re-fetches", r.inc.enqueued)
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
