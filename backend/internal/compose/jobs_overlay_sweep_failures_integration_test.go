// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The reconcile sweep's failure policy, proved per phase against a real
// Postgres: which incumbent failures are tolerated (log, skip the class, keep
// sweeping) and which abort the whole connection sweep so the poller backs it
// off. The distinction lives in sweepMustStop/overlaySweepAborts
// (jobs_overlay.go, overlay_sweep_policy.go); getting it backwards either
// quarantines a healthy workspace over one bad object, or re-sweeps a
// rate-limited/auth-rejected connection hot every tick.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/fake"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// errUnmappablePayload stands in for a PER-OBJECT incumbent defect — a
// payload this adapter cannot map, a portal-shaped validation refusal. It
// deliberately satisfies none of isConnectionLevelIncumbentError's sentinels,
// because that is exactly what makes it tolerable: one object class's data
// problem says nothing about the connection's health.
var errUnmappablePayload = errors.New("fake: unmappable record payload")

// phaseFailingIncumbent fails ONE phase of every object class's sweep —
// Backfill, Modified, or Deletions — and passes every other call through to
// the embedded fake. It is how a test drives the per-phase failure policy
// deterministically: the failure has to come from the incumbent seam (a real
// HubSpot returning 403/429/500 is not something a test can arrange), while
// the phases around it must keep working, or the test could not tell "this
// phase was skipped" from "the sweep stopped".
type phaseFailingIncumbent struct {
	*fake.Adapter
	backfillErr  error
	modifiedErr  error
	deletionsErr error
}

func (p *phaseFailingIncumbent) Backfill(ctx context.Context, objectClass, cursor string) (overlay.Page, error) {
	if p.backfillErr != nil {
		return overlay.Page{}, p.backfillErr
	}
	return p.Adapter.Backfill(ctx, objectClass, cursor)
}

func (p *phaseFailingIncumbent) Modified(ctx context.Context, objectClass string, since time.Time, cursor string) (overlay.Page, error) {
	if p.modifiedErr != nil {
		return overlay.Page{}, p.modifiedErr
	}
	return p.Adapter.Modified(ctx, objectClass, since, cursor)
}

func (p *phaseFailingIncumbent) Deletions(ctx context.Context, objectClass string, since time.Time, cursor string) (overlay.DeletionPage, error) {
	if p.deletionsErr != nil {
		return overlay.DeletionPage{}, p.deletionsErr
	}
	return p.Adapter.Deletions(ctx, objectClass, since, cursor)
}

// dueConnectionFor answers the workspace's own entry in the fleet-wide
// due-connection scan — the value the periodic worker hands
// reconcileConnection, carrying the credential ref, region, and the
// connected_at every fenced write is checked against.
func dueConnectionFor(ctx context.Context, t *testing.T, pool *pgxpool.Pool, ws ids.UUID) overlay.DueOverlayConnection {
	t.Helper()
	due, err := overlay.DueOverlayConnections(ctx, pool)
	if err != nil {
		t.Fatalf("DueOverlayConnections: %v", err)
	}
	for _, c := range due {
		if c.Workspace.UUID == ws {
			return c
		}
	}
	t.Fatal("no due overlay connection for the workspace after connect")
	return overlay.DueOverlayConnection{}
}

// TestReconcileConnectionPerPhaseFailurePolicy walks each sweep phase twice:
// once failing it with a per-object defect (tolerated — the class is logged
// and skipped, the sweep converges so the poller resets its backoff) and once
// with a connection-level failure (aborts — the poller records a backoff
// instead of re-sweeping a dead or throttled connection every tick). The
// mirror assertion is the half a return-value check alone would miss: a
// tolerated failure must leave the phases that DID run intact, and an abort
// must not undo them either.
func TestReconcileConnectionPerPhaseFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		// inject arms exactly one phase's failure before the sweep runs.
		inject func(*phaseFailingIncumbent)
		// wantErr is the sentinel reconcileConnection must surface, nil when
		// the failure is tolerated and the sweep converges.
		wantErr error
		// wantMirrored is whether the seeded record reaches the mirror — false
		// only when the failing phase is the one that would have carried it.
		wantMirrored bool
	}{
		{
			name:         "a per-object backfill failure skips the class, the sweep converges",
			inject:       func(p *phaseFailingIncumbent) { p.backfillErr = errUnmappablePayload },
			wantErr:      nil,
			wantMirrored: false, // the initial load never converged, so nothing landed
		},
		{
			name:         "an auth-rejected backfill aborts the whole sweep",
			inject:       func(p *phaseFailingIncumbent) { p.backfillErr = apperrors.ErrPermissionDenied },
			wantErr:      apperrors.ErrPermissionDenied,
			wantMirrored: false,
		},
		{
			name:         "a per-object modified-sweep failure skips the class, the sweep converges",
			inject:       func(p *phaseFailingIncumbent) { p.modifiedErr = errUnmappablePayload },
			wantErr:      nil,
			wantMirrored: true, // the backfill phase ran first and landed the record
		},
		{
			name:         "an unreachable incumbent in the modified sweep aborts the whole sweep",
			inject:       func(p *phaseFailingIncumbent) { p.modifiedErr = hubspot.ErrUnreachable },
			wantErr:      hubspot.ErrUnreachable,
			wantMirrored: true,
		},
		{
			name:         "a per-object deletion-sweep failure skips the class, the sweep converges",
			inject:       func(p *phaseFailingIncumbent) { p.deletionsErr = errUnmappablePayload },
			wantErr:      nil,
			wantMirrored: true,
		},
		{
			name:         "an exhausted incumbent budget in the deletion sweep aborts the whole sweep",
			inject:       func(p *phaseFailingIncumbent) { p.deletionsErr = apperrors.ErrIncumbentBudgetExhausted },
			wantErr:      apperrors.ErrIncumbentBudgetExhausted,
			wantMirrored: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := integration.Setup(t)
			vault := keyvault.NewMemory()
			ms := overlay.NewMirrorStore(e.Pool, unresolvedOwnerEmails{})
			adminCtx := overlayAdminCtx(e.WS, e.Rep1)
			if _, err := overlay.NewService(e.Pool, vault, ms).
				Connect(adminCtx, overlay.ConnectInput{Incumbent: "hubspot", Region: "eu1", Token: "tok"}); err != nil {
				t.Fatalf("Connect: %v", err)
			}

			// One owned contacts record: the owners directory match (Rep1 is
			// a@authz.test in the shared harness) is what makes the mirrored row
			// readable, so the mirror assertion below measures the sweep and not a
			// missing mapping.
			inc := &phaseFailingIncumbent{Adapter: fake.New()}
			inc.SeedOwner("owner-1", "a@authz.test")
			rec := fake.Rec("c-1", map[string]any{"firstname": "Ada"})
			rec.ObjectClass, rec.OwnerExternalID = "person", "owner-1"
			rec.ModifiedAt = time.Now().Add(-24 * time.Hour)
			inc.Seed(overlay.IncumbentClassContacts, rec)
			tc.inject(inc)

			d := dueConnectionFor(adminCtx, t, e.Pool, e.WS)
			sweepCtx := reconcileWorkerCtx(context.Background(), ids.From[ids.WorkspaceKind](e.WS))
			err := reconcileConnection(sweepCtx, e.Pool, vault, ms, workerBudgetMeter(t),
				slog.New(slog.DiscardHandler), d, func(_, _ string) overlay.Incumbent { return inc })

			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("reconcileConnection = %v, want nil — a per-object failure must not abort the connection sweep", err)
			case tc.wantErr != nil && !errors.Is(err, tc.wantErr):
				t.Fatalf("reconcileConnection = %v, want %v — a connection-level failure must abort so the poller backs off", err, tc.wantErr)
			}

			_, getErr := ms.Get(overlayReaderCtx(e.WS, e.Rep1), "person", "c-1")
			if tc.wantMirrored && getErr != nil {
				t.Errorf("the record must still be mirrored by the phases that ran, got: %v", getErr)
			}
			if !tc.wantMirrored && !errors.Is(getErr, apperrors.ErrNotFound) {
				t.Errorf("the failing phase was the one that carries the record; want ErrNotFound, got: %v", getErr)
			}
		})
	}
}
