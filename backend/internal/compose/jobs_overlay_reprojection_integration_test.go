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

// sweepContacts runs one full contacts sweep — every phase, in order — the way
// reconcileConnection composes it for a live connection.
func (r *reprojectionEnv) sweepContacts(t *testing.T) {
	t.Helper()
	deps := sweepDeps{
		inc:     r.inc,
		ms:      r.ms.WithResolver(r.inc).WithFenceIdentity(r.due.ConnectedAt),
		meter:   r.meter,
		enqueue: r.inc,
		log:     slog.New(slog.DiscardHandler),
	}
	if err := sweepObjectClass(r.sweepCtx, deps, r.due.Workspace, overlay.IncumbentClassContacts, r.due.ConnectedAt); err != nil {
		t.Fatalf("sweepObjectClass: %v", err)
	}
}

func TestSweepReprojectionEnqueuesOnlyTheRowsAnOlderDeclarationProjected(t *testing.T) {
	r := setupReprojection(t)
	r.sweepContacts(t)

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
	r.sweepContacts(t)
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
	r.sweepContacts(t)
	if len(r.inc.enqueued) != 0 {
		t.Fatalf("the second sweep enqueued %v, want none — a converged class must cost nothing every tick", r.inc.enqueued)
	}
}
