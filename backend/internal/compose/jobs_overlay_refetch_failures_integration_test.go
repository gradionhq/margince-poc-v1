// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The webhook-as-signal re-fetch worker's failure paths, proved against a real
// Postgres. A signal is an optimization: the poller heals whatever it misses,
// so every condition that makes a single-record refresh pointless — the
// workspace disconnected, the mirror halted, the record gone — must be a clean
// stop that reaches (or ingests) nothing, while a genuinely transient failure
// must surface so River retries it. The happy path lives beside the other
// worker proofs in jobs_overlay_worker_integration_test.go.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget"
	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// refetchFixture stands up the state a webhook signal arrives into: a
// connected HubSpot overlay whose owner map the poller has already seeded, so
// a re-fetched record is readable by Rep1 and "not mirrored" means the worker
// really did stop rather than merely lose a visibility match.
type refetchFixture struct {
	e     *integration.Env
	vault keyvault.Vault
	ms    *overlay.MirrorStore
	meter *overlaybudget.Meter
}

func newRefetchFixture(t *testing.T) refetchFixture {
	t.Helper()
	e := integration.Setup(t)
	vault := keyvault.NewMemory()
	ms := overlay.NewMirrorStore(e.DB(), unresolvedOwnerEmails{})
	adminCtx := overlayAdminCtx(e.WS, e.Rep1)
	if _, err := overlay.NewService(e.DB(), vault, ms).
		Connect(adminCtx, overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	dir := fake.New()
	dir.SeedOwner("owner-1", "a@authz.test")
	if err := ms.WithResolver(dir).SeedUserMap(adminCtx, incumbentHubSpot,
		[]overlay.OwnerRef{{ExternalID: "owner-1", Email: "a@authz.test"}}); err != nil {
		t.Fatalf("SeedUserMap: %v", err)
	}
	// A HubSpot-configured meter so the worker's ReserveREST is allowed rather
	// than shed — a shed would skip the incumbent read these tests are about.
	return refetchFixture{e: e, vault: vault, ms: ms, meter: budgettest.Meter(t, budgettest.SmallConfig("hubspot"))}
}

// worker takes THIS build's registered re-fetch worker and points it at inc,
// exposed (rather than folded into signal) so a test can vary one dependency —
// the vault, say — before the run.
func (f refetchFixture) worker(t *testing.T, inc overlay.Incumbent) *overlayRefetchWorker {
	t.Helper()
	return registeredRefetchWorker(t, f.e.Pool, f.vault, f.meter, inc)
}

// signal executes one coalesced webhook signal for contacts/c-1 — the record
// every portal below seeds.
func (f refetchFixture) signal(w *overlayRefetchWorker) error {
	return w.Work(context.Background(), &river.Job[OverlayRefetchArgs]{
		Args: OverlayRefetchArgs{
			Workspace: f.e.WS, IncumbentClass: overlay.IncumbentClassContacts, ExternalID: "c-1",
		},
	})
}

// mirrored reports whether Rep1 — the seed-mapped owner — can read the
// re-fetched record, the honest measure of whether the worker ingested.
func (f refetchFixture) mirrored(t *testing.T) bool {
	t.Helper()
	_, err := f.ms.Get(overlayReaderCtx(f.e.WS, f.e.Rep1), "person", "c-1")
	if err != nil && !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("reading the mirror: %v", err)
	}
	return err == nil
}

// seededPortal is an incumbent that HAS the signalled record, owned by the
// seed-matched owner. Every stop-path test below uses it, so the record's
// absence afterward can only mean the worker stopped — never that there was
// nothing to fetch.
func seededPortal() *fake.Adapter {
	inc := fake.New()
	inc.SeedOwner("owner-1", "a@authz.test")
	rec := fake.Rec("c-1", map[string]any{"firstname": "Ada"})
	rec.ObjectClass, rec.OwnerExternalID = "person", "owner-1"
	rec.ModifiedAt = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	inc.Seed(overlay.IncumbentClassContacts, rec)
	return inc
}

// TestOverlayRefetchWorkerStopsCleanlyWhenTheWorkspaceDisconnected proves the
// pre-flight the coalescing window makes necessary: a signal is scheduled a
// short time ahead, so a disconnect can land between the webhook and the job.
// Teardown owns the mirror at that point — the worker must reach the incumbent
// for nothing and re-mirror nothing into a now-native workspace, and must not
// report a failure River would retry.
func TestOverlayRefetchWorkerStopsCleanlyWhenTheWorkspaceDisconnected(t *testing.T) {
	f := newRefetchFixture(t)
	if err := overlay.NewService(f.e.DB(), f.vault, f.ms).
		Disconnect(overlayAdminCtx(f.e.WS, f.e.Rep1)); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	spy := &getCountingIncumbent{Adapter: seededPortal()}
	if err := f.signal(f.worker(t, spy)); err != nil {
		t.Fatalf("a signal for a disconnected workspace must be a clean stop, got: %v", err)
	}
	if spy.gets != 0 {
		t.Errorf("a disconnected workspace must not be read from the incumbent, got %d Get call(s)", spy.gets)
	}
	if f.mirrored(t) {
		t.Error("nothing may be re-mirrored into a workspace teardown has already reverted to native")
	}
}

// TestOverlayRefetchWorkerSkipsAHaltedMirror proves the halt is re-checked at
// EXECUTION, not only when the signal was received: a ledger value-hash
// collision (OVA-AC-3) halts the mirror, and a signal coalesced before that
// halt must still not spend a live incumbent read on a write ingestTx would
// refuse anyway.
func TestOverlayRefetchWorkerSkipsAHaltedMirror(t *testing.T) {
	f := newRefetchFixture(t)
	adminCtx := overlayAdminCtx(f.e.WS, f.e.Rep1)
	// The halt is set by the write ledger on collision; there is no operator
	// unhalt to drive it through, so insert the row the way the ledger does.
	if err := database.WithWorkspaceTx(adminCtx, f.e.Pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(adminCtx, `
			INSERT INTO overlay_mirror_halt (reason)
			VALUES ('value-hash collision')`)
		return execErr
	}); err != nil {
		t.Fatalf("halting the mirror: %v", err)
	}

	spy := &getCountingIncumbent{Adapter: seededPortal()}
	if err := f.signal(f.worker(t, spy)); err != nil {
		t.Fatalf("a signal for a halted mirror must be a clean stop, got: %v", err)
	}
	if spy.gets != 0 {
		t.Errorf("a halted mirror must not be read from the incumbent, got %d Get call(s)", spy.gets)
	}
	if f.mirrored(t) {
		t.Error("a halted mirror must not be written to")
	}
}

// TestOverlayRefetchWorkerLeavesAnUnreadableRecordToThePoller proves the
// worker distinguishes "gone" from "broken": a record the incumbent will not
// return — archived between the signal and the job — is not retryable, since
// the same read gives the same answer however many attempts it is given, so
// the worker stops cleanly and leaves the row to the poller's deletion feed.
func TestOverlayRefetchWorkerLeavesAnUnreadableRecordToThePoller(t *testing.T) {
	f := newRefetchFixture(t)
	// A portal WITHOUT the signalled record: its Get fails with a plain
	// not-retryable error, exactly like an archived record.
	empty := fake.New()
	empty.SeedOwner("owner-1", "a@authz.test")

	if err := f.signal(f.worker(t, empty)); err != nil {
		t.Fatalf("an unreadable record must not be retried, got: %v", err)
	}
	if f.mirrored(t) {
		t.Error("a record the incumbent would not return must not appear in the mirror")
	}
}

// getFailingIncumbent fails the single-record read with a chosen error, which
// is the only way to put the worker in front of a specific read failure: a
// rate-limited, auth-rejected or unreachable incumbent cannot be arranged
// against a real portal here, and neither can a record the incumbent serves
// whole that this build's declaration cannot project.
type getFailingIncumbent struct {
	*fake.Adapter
	err error
}

func (g getFailingIncumbent) Get(context.Context, string, string) (overlay.Record, error) {
	return overlay.Record{}, g.err
}

// retiredIncumbentClass is an object class this build declares no mapping for.
// A queued re-fetch can still name one: river_job outlives a deploy, so a job
// enqueued while a mapping was declared is worked by the build that retired it.
const retiredIncumbentClass = "tickets"

// undeclaredClassReadError is what a real adapter answers for a class this
// build declares no mapping for: it refuses at its own declaration lookup,
// before any incumbent call, which is why such a read reaches the disposal
// carrying no unprojectable mark. Taken from the adapter rather than spelled
// out here, so the worker is judged against the error production hands it.
func undeclaredClassReadError(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("the adapter called the incumbent for %s, a class it declares no mapping for", retiredIncumbentClass)
	}))
	defer srv.Close()

	_, err := hubspot.NewAdapter(hubspot.NewClient("eu1", "tok", hubspot.WithBaseURL(srv.URL))).
		Get(context.Background(), retiredIncumbentClass, "c-1")
	if err == nil {
		t.Fatalf("Get(%s) succeeded, so this build declares that class after all and the case below is not the one under test",
			retiredIncumbentClass)
	}
	if errors.Is(err, hubspot.ErrUnmappable) {
		t.Fatalf("Get(%s) = %v, already marked unprojectable — the case under test is the one settled before that mark is read",
			retiredIncumbentClass, err)
	}
	return err
}

// TestOverlayRefetchWorkerReportsAClassNoDeclarationCovers proves the disposal
// of a read that failed because this build declares no mapping for the class it
// named — a job enqueued before a build retired that mapping, since river_job
// outlives a deploy. Nothing can project such a record, so there is no
// declaration to record and none to record it against: the worker stops
// cleanly, writes nothing, and names the class, which is the only trace an
// outcome that writes nothing can leave for whoever finds the rows standing
// still.
func TestOverlayRefetchWorkerReportsAClassNoDeclarationCovers(t *testing.T) {
	f := newRefetchFixture(t)
	var logged bytes.Buffer
	w := f.worker(t, getFailingIncumbent{Adapter: seededPortal(), err: undeclaredClassReadError(t)})
	w.log = slog.New(slog.NewTextHandler(&logged, nil))

	if err := w.Work(context.Background(), &river.Job[OverlayRefetchArgs]{Args: OverlayRefetchArgs{
		Workspace: f.e.WS, IncumbentClass: retiredIncumbentClass, ExternalID: "c-1",
	}}); err != nil {
		t.Fatalf("a class no declaration covers must not be retried — no attempt reaches a mapping that is not in the "+
			"binary — got: %v", err)
	}
	if f.mirrored(t) {
		t.Error("a read that never came back must ingest nothing")
	}
	if !strings.Contains(logged.String(), retiredIncumbentClass) ||
		!strings.Contains(logged.String(), "no mapping declaration for this object class") {
		t.Fatalf("the worker logged %q, want it naming %s and saying no declaration covers it — reported as a record the "+
			"incumbent withheld, the job reads as one the next pass will heal, and nothing ever will",
			logged.String(), retiredIncumbentClass)
	}
}

// TestOverlayRefetchWorkerRetriesAConnectionLevelReadFailure is the other side
// of the test above: a WHOLE-connection failure (auth here) is transient from
// the job's point of view, so it must surface as an error for River to back off
// and retry — swallowing it would silently drop the signal.
func TestOverlayRefetchWorkerRetriesAConnectionLevelReadFailure(t *testing.T) {
	f := newRefetchFixture(t)
	err := f.signal(f.worker(t, getFailingIncumbent{Adapter: seededPortal(), err: apperrors.ErrPermissionDenied}))
	if !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("a connection-level read failure = %v, want it surfaced so River retries", err)
	}
	if f.mirrored(t) {
		t.Error("a failed read must ingest nothing")
	}
}

// TestOverlayRefetchWorkerSurfacesAnUnresolvableToken proves the vault
// resolution failure is never swallowed into a silent no-op: the connection is
// active, so the secret SHOULD be there, and a missing one is an environment
// failure worth retrying — not a record that stopped existing.
func TestOverlayRefetchWorkerSurfacesAnUnresolvableToken(t *testing.T) {
	f := newRefetchFixture(t)
	w := f.worker(t, seededPortal())
	w.vault = keyvault.NewMemory() // a vault that never held this connection's secret
	if err := f.signal(w); err == nil {
		t.Fatal("an unresolvable vaulted token must surface as an error, not a silent no-op")
	}
}

// revokeOnGetIncumbent revokes the workspace's connection row (leaving the
// vaulted token in place) as its single-record read returns — a disconnect
// landing after the worker's pre-flight passed but before its write, the
// window WithFenceIdentity exists for. The fence is DB state, not an incumbent
// response, so this is the only place a test can open that window
// deterministically.
type revokeOnGetIncumbent struct {
	*fake.Adapter
	pool *pgxpool.Pool
}

func (r *revokeOnGetIncumbent) Get(ctx context.Context, objectClass, externalID string) (overlay.Record, error) {
	rec, err := r.Adapter.Get(ctx, objectClass, externalID)
	if err != nil {
		return rec, err
	}
	if err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx,
			`UPDATE incumbent_connection SET status = 'revoked', revoked_at = now()`)
		return execErr
	}); err != nil {
		return overlay.Record{}, err
	}
	return rec, nil
}

// TestOverlayRefetchWorkerStopsCleanlyOnADisconnectMidRefetch proves the fence
// covers the ingest too, not just the pre-flight: a disconnect that lands after
// the record is already in hand aborts the write with ErrConnectionGone, which
// the worker turns into a clean stop — so a signal cannot resurrect a mirror
// row into a workspace teardown has already reverted to native.
func TestOverlayRefetchWorkerStopsCleanlyOnADisconnectMidRefetch(t *testing.T) {
	f := newRefetchFixture(t)
	inc := &revokeOnGetIncumbent{Adapter: seededPortal(), pool: f.e.Pool}
	if err := f.signal(f.worker(t, inc)); err != nil {
		t.Fatalf("a disconnect mid-refetch must be a clean stop, got: %v", err)
	}
	if f.mirrored(t) {
		t.Error("the fence must abort the ingest — nothing may land under a revoked connection")
	}
}
