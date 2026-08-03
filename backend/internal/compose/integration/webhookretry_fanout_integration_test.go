// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The outbound-webhook retry sweep is one job row per live tenant. A workspace
// whose due-retry scan fails must say so: a sweep that reported success is
// indistinguishable from a sweep that found nothing due, and what it hides is a
// tenant whose parked deliveries are never re-attempted at all — the subscriber
// simply stops receiving, with no failed row anywhere to read.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/webhooks"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// webhookSweepCtx is the scope the retry workspace worker binds before it calls
// the engine: the tenant, and nothing else. The sweep re-sends deliveries that
// were already authorized against their owner's scope when they were enqueued,
// so it resolves no principal and writes no audited row — a suite that bound
// more would be exercising a pass production never runs.
func webhookSweepCtx(ws ids.UUID) context.Context {
	return principal.WithWorkspaceID(context.Background(), ws)
}

// failDueScansFor makes reading a webhook_delivery row raise inside ONE
// tenant's transactions, leaving every other tenant's reads untouched. The
// victim must therefore have a delivery the scan would return: the policy is
// evaluated as the scan reads rows, so a tenant with nothing due sweeps
// cleanly even while armed.
//
// A RESTRICTIVE row-level policy is the fault seam, not the trigger the other
// fan-out suites use: the only workspace-level failure this pass can suffer is
// its due SCAN failing, and no trigger fires on a SELECT. The predicate names
// the victim through the workspace GUC the sweep's own transaction binds
// rather than through the row's own column, so no other tenant's scan can trip
// over it whatever order the planner evaluates the quals in.
//
// It is dropped in cleanup — the integration lane resets rows between tests but
// keeps the schema, so a surviving policy would blind every later suite that
// reads a delivery.
func failDueScansFor(t *testing.T, owner *pgx.Conn, ws ids.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `
		CREATE OR REPLACE FUNCTION webhook_due_scan_fault(victim uuid) RETURNS boolean
		LANGUAGE plpgsql AS $$
		BEGIN
		  IF current_setting('app.workspace_id', true) = victim::text THEN
		    RAISE EXCEPTION 'webhook due-scan fault injection';
		  END IF;
		  RETURN true;
		END $$`); err != nil {
		t.Fatalf("creating the fault-injection function: %v", err)
	}
	// Registered before the policy is armed, not after both: a failure to arm
	// would otherwise leave the function behind, which is the leak this
	// helper's whole cleanup exists to prevent. Cleanups run LIFO, so the
	// policy below still drops first.
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DROP FUNCTION webhook_due_scan_fault(uuid)`); err != nil {
			t.Errorf("dropping the fault-injection function: %v", err)
		}
	})
	// CREATE POLICY takes no bind parameters, so the tenant is interpolated;
	// it is a UUID rendered by ids.UUID.String(), never caller text.
	if _, err := owner.Exec(ctx, `
		CREATE POLICY webhook_delivery_scan_fault ON webhook_delivery
		AS RESTRICTIVE FOR SELECT
		USING (webhook_due_scan_fault('`+ws.String()+`'::uuid))`); err != nil {
		t.Fatalf("arming the fault-injection policy: %v", err)
	}
	t.Cleanup(func() {
		if _, err := owner.Exec(context.Background(),
			`DROP POLICY webhook_delivery_scan_fault ON webhook_delivery`); err != nil {
			t.Errorf("dropping the fault-injection policy: %v", err)
		}
	})
}

// parkOneDelivery registers a subscription against a receiver that is down and
// drives one failed attempt, leaving exactly one delivery parked for retry.
func parkOneDelivery(t *testing.T, we *webhookEnv, deliverer *webhooks.Deliverer, rcv *receiver) {
	t.Helper()
	subID, _ := we.createSubscription(t, rcv.server.URL+"/hook", []string{"deal.created"})
	if err := deliverer.HandleEvent(context.Background(), makeEnvelope(we.wsID, "deal.created")); err != nil {
		t.Fatalf("the first delivery attempt: %v", err)
	}
	assertDeliveryStatus(t, we, subID, "retrying", 1)
}

// TestWebhookRetryReportsTheWorkspaceWhoseDueScanFailed is the
// characterization: a tenant whose due-retry scan hits a database error must
// reach its caller as an error. A sweep that reported success over it leaves
// every delivery parked in that tenant parked forever, and records a healthy
// pass while doing so.
func TestWebhookRetryReportsTheWorkspaceWhoseDueScanFailed(t *testing.T) {
	we := setupWebhooks(t)
	owner := OwnerConn(t)
	healthy := seedExtraWorkspace(t, owner, "healthy", false)

	rcv := newReceiver(t, http.StatusInternalServerError) // endpoint is down
	now := time.Now().UTC()
	deliverer := newTestDeliverer(we, &now, rcv.server.Client())
	parkOneDelivery(t, we, deliverer, rcv)

	// One healthy pass first: it proves the parked delivery is reachable by a
	// sweep at this clock reading, so the "not re-attempted" assertion below
	// says the fault held rather than that there was nothing to attempt.
	now = now.Add(64 * time.Second) // beyond the largest backoff gap
	if err := deliverer.SweepOnce(webhookSweepCtx(we.wsID)); err != nil {
		t.Fatalf("the sweep before any fault was injected: %v", err)
	}
	if got := rcv.count.Load(); got != 2 {
		t.Fatalf("the endpoint saw %d attempts, want the enqueue attempt plus one re-attempt — the sweep never reached the parked delivery", got)
	}

	failDueScansFor(t, owner, we.wsID)
	now = now.Add(64 * time.Second)

	err := deliverer.SweepOnce(webhookSweepCtx(we.wsID))
	if got := rcv.count.Load(); got != 2 {
		t.Fatalf("the fault injection did not hold: the victim's parked delivery was re-attempted %d more times, so its due scan never failed", got-2)
	}
	if err == nil {
		t.Fatal("a workspace whose due-retry scan failed reported success — every delivery parked in that tenant stays parked and nothing records that the sweep never scanned")
	}
	// The fault is one tenant's, and the pass now takes the tenant it is given:
	// the healthy workspace's sweep is unaffected by the victim's failure, which
	// is what the old fleet loop's swallowed error was justified by.
	if err := deliverer.SweepOnce(webhookSweepCtx(healthy)); err != nil {
		t.Fatalf("the healthy tenant's sweep: %v", err)
	}
}

// TestWebhookRetryFansOutOneJobPerLiveWorkspaceAndFailsOnlyTheFailedTenant is
// the converted shape: the dispatcher enqueues one row per LIVE workspace —
// archived ones excluded, because a delivery parked for a subscription nobody
// is listening on any more is work nobody wants — each row names its own tenant
// on the wire, and the tenant whose scan failed is the only row that fails.
func TestWebhookRetryFansOutOneJobPerLiveWorkspaceAndFailsOnlyTheFailedTenant(t *testing.T) {
	we := setupWebhooks(t)
	owner := OwnerConn(t)
	healthy := seedExtraWorkspace(t, owner, "healthy", false)
	archived := seedExtraWorkspace(t, owner, "archived", true)

	rcv := newReceiver(t, http.StatusInternalServerError)
	now := time.Now().UTC()
	deliverer := newTestDeliverer(we, &now, rcv.server.Client())
	parkOneDelivery(t, we, deliverer, rcv)
	// Past the parked delivery's backoff, so the victim's sweep has something
	// due to scan for: a tenant with nothing due never reads a row and so never
	// meets the fault.
	now = now.Add(64 * time.Second)
	// Permanent, not transient: the row fires a failure event on every attempt,
	// and a fault that healed would let attempt 2 complete and record the
	// tenant as green — the exact outcome this test denies.
	failDueScansFor(t, owner, we.wsID)

	_, completed, failed := startTestJobRunner(t, we.pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		WebhookRetry: compose.WebhookRetryConfig{
			Interval: time.Hour, Deliverer: deliverer,
		},
	})
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	kind := compose.WebhookRetryWorkspaceArgs{}.Kind()
	outcomes := awaitWorkspaceJobOutcomes(waitCtx, t, completed, failed, kind, 2)

	if _, fannedOut := outcomes[healthy.String()]; !fannedOut {
		t.Errorf("no retry sweep ran for workspace %s — a tenant the fan-out skipped keeps its deliveries parked and no row records that it did", healthy)
	}
	if !outcomes[healthy.String()] {
		t.Error("the healthy tenant's retry sweep failed")
	}
	if outcomes[we.wsID.String()] {
		t.Error("the tenant whose due scan could not run reported a completed job — the failure the per-workspace row exists to record was swallowed")
	}

	// The archived tenant must have no row at all. This count is fenced on the
	// two outcomes above rather than read early: the fan-out is ONE atomic
	// InsertMany, so any child reporting proves that insert committed — and it
	// carried every workspace the dispatcher enumerated.
	var dispatched int
	if err := we.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1 AND args->>'workspace_id' = $2`,
		kind, archived.String()).Scan(&dispatched); err != nil {
		t.Fatalf("counting the archived tenant's retry jobs: %v", err)
	}
	if dispatched != 0 {
		t.Errorf("%d %s rows were dispatched for an ARCHIVED workspace — re-attempting a delivery for a subscription nobody listens on any more spends outbound attempts on work nobody wants", dispatched, kind)
	}
}

// TestWebhookRetryDispatchRepeatsOnItsConfiguredInterval pins the half of the
// schedule a boot pass hides. RunOnStart fires once whatever the cadence is, so
// a dispatcher wired to a constant instead of the operator's
// --webhook-retry-interval looks identical at boot and then never runs again —
// every parked delivery in the fleet stranded, with every gate green. Two
// dispatches inside this window can only happen if the configured interval is
// what River is scheduling on.
func TestWebhookRetryDispatchRepeatsOnItsConfiguredInterval(t *testing.T) {
	we := setupWebhooks(t)
	now := time.Now().UTC()
	rcv := newReceiver(t, http.StatusInternalServerError)

	_, completed, _ := startTestJobRunner(t, we.pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		WebhookRetry: compose.WebhookRetryConfig{
			Interval: 2 * time.Second, Deliverer: newTestDeliverer(we, &now, rcv.server.Client()),
		},
	})
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	kind := compose.WebhookRetryArgs{}.Kind()
	dispatched := make(map[int64]struct{}, 2)
	for len(dispatched) < 2 {
		select {
		case <-waitCtx.Done():
			t.Fatalf("saw %d of 2 %s dispatches: %v — the sweep fired at boot and then never again, so the operator's interval is not what schedules it",
				len(dispatched), kind, waitCtx.Err())
		case ev := <-completed:
			if ev != nil && ev.Job != nil && ev.Job.Kind == kind {
				dispatched[ev.Job.ID] = struct{}{}
			}
		}
	}
}

// TestWebhookRetryWithoutAnIntervalSchedulesNothingButStillWorksAQueuedRow pins
// the omission. River accepts PeriodicInterval(0) and turns it into a schedule
// whose next run time never advances, so a runner assembled by a caller that
// never meant to sweep would fan the whole fleet out as fast as Postgres
// accepts an insert. Registering no schedule is the honest reading; the WORKERS
// still register, so a row an earlier boot queued is still worked rather than
// stranded.
func TestWebhookRetryWithoutAnIntervalSchedulesNothingButStillWorksAQueuedRow(t *testing.T) {
	we := setupWebhooks(t)
	now := time.Now().UTC()
	rcv := newReceiver(t, http.StatusInternalServerError)
	deliverer := newTestDeliverer(we, &now, rcv.server.Client())

	runner, completed, _ := startTestJobRunner(t, we.pool, compose.JobRunnerConfig{
		CloseDateInterval: time.Hour,
		ReconcileInterval: time.Hour,
		TimeScanInterval:  time.Hour,
		WebhookRetry:      compose.WebhookRetryConfig{Interval: 0, Deliverer: deliverer},
	})
	if err := runner.Enqueue(context.Background(),
		compose.WebhookRetryWorkspaceArgs{Workspace: we.wsID}, nil); err != nil {
		t.Fatalf("enqueueing the workspace pass an earlier boot would have left: %v", err)
	}

	// The close-date sweep is the FENCE, and it has to be: River inserts every
	// RunOnStart periodic job in one round after Start returns, so a run that
	// only waited on the hand-queued row could read the count before that round
	// had happened at all — and would then report zero however the schedule was
	// wired. Waiting for a sibling RunOnStart dispatcher to complete puts the
	// round provably in the past. The workspace pass is waited on for the other
	// half of the claim: a queued row is still worked with no schedule present.
	waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	awaitKindsCompleted(waitCtx, t, completed,
		compose.CloseDateSweepArgs{}.Kind(), compose.WebhookRetryWorkspaceArgs{}.Kind())

	var dispatched int
	if err := we.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM river_job WHERE kind = $1`,
		compose.WebhookRetryArgs{}.Kind()).Scan(&dispatched); err != nil {
		t.Fatalf("counting the dispatched retry passes: %v", err)
	}
	if dispatched != 0 {
		t.Errorf("%d %s rows were dispatched by a runner given no retry interval — a zero duration is not a cadence, and River spins on it rather than refusing it",
			dispatched, compose.WebhookRetryArgs{}.Kind())
	}
}
