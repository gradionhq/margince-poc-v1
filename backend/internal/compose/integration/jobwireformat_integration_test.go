// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// What River's encoder actually writes into river_job.args, read back off a
// real Postgres.
//
// The struct-tag gate over the args types reads tag TEXT and stops there. It
// cannot see a marshaled row, and the per-workspace reads of the job table
// select `args->>'workspace_id'` rather than the Go field — so a tag the gate
// approves and an encoder that ships some other key would both read green
// while the query returned null for work a tenant really did.
//
// The claim is a biconditional: a non-null workspace_id means tenant work, a
// null means a dispatcher. A dispatcher therefore rides in the same batch. A
// test that only checked the key appears would pass on a system that shipped
// it everywhere, which is the failure that lets a fleet fan-out be counted as
// one workspace's pass.

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// workspaceKeysOnTheWire reads each job enqueued above the given high-water
// mark as kind → the workspace_id its args ship, nil when the key is absent.
// The extraction is the production shape — the text operator the later
// per-workspace queries use, not a decode back into the Go struct, which would
// agree with itself whatever key the encoder chose. river_job is not
// workspace-scoped, so it is read off the pool directly.
func workspaceKeysOnTheWire(t *testing.T, e *Env, highWater int64) map[string]*string {
	t.Helper()
	rows, err := e.Pool.Query(context.Background(),
		`SELECT kind, args->>'workspace_id' FROM river_job WHERE id > $1`, highWater)
	if err != nil {
		t.Fatalf("reading back the enqueued jobs: %v", err)
	}
	type wireRow struct {
		Kind      string
		Workspace *string
	}
	wire, err := pgx.CollectRows(rows, pgx.RowToStructByPos[wireRow])
	if err != nil {
		t.Fatalf("collecting the enqueued jobs: %v", err)
	}
	onWire := make(map[string]*string, len(wire))
	for _, row := range wire {
		if _, duplicate := onWire[row.Kind]; duplicate {
			t.Fatalf("kind %s landed twice in one batch — one of the two would be hidden by the kind-keyed lookup", row.Kind)
		}
		onWire[row.Kind] = row.Workspace
	}
	return onWire
}

func TestRiverShipsTheWorkspaceUnderTheKeyThePerWorkspaceReadsSelect(t *testing.T) {
	e := Setup(t)
	ensureRiverSchema(t)
	inserter, err := jobs.NewInserter(e.Pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("jobs.NewInserter: %v", err)
	}
	ctx := context.Background()

	// Every kind carries a DISTINCT workspace, so the assertion is that the
	// value read back for a kind is the value that kind was enqueued with.
	// Sharing one id across the batch would still hold if the encoder emitted
	// a neighbouring row's workspace, and a non-null check alone would hold
	// for the zero UUID — the two ways a workspace key can be present and
	// still mean nothing.
	tenantJobs := []jobs.WorkspaceScoped{
		compose.SendEmailArgs{Workspace: ids.NewV7(), DeliveryID: ids.NewV7().String()},
		compose.CaptureBackfillArgs{Workspace: ids.NewV7(), BackfillID: ids.NewV7().String()},
		compose.TelegramIngestArgs{
			Workspace: ids.NewV7(), ConnectionID: ids.NewV7().String(),
			BotID: "bot", RawCaptureID: ids.NewV7().String(),
		},
		compose.TelegramPollArgs{Workspace: ids.NewV7(), ConnectionID: ids.NewV7().String()},
		compose.VoiceBuildArgs{
			Workspace: ids.NewV7(), ProfileID: ids.NewV7().String(),
			BuildID: ids.NewV7().String(), RequestedBy: "human:" + e.Rep1.String(),
		},
		compose.CaptureSyncArgs{
			Workspace: ids.NewV7(), ConnectionID: ids.NewV7().String(), Provider: "gmail",
		},
		compose.OverlayRefetchArgs{
			Workspace: ids.NewV7(), IncumbentClass: "hubspot", ExternalID: ids.NewV7().String(),
		},
	}
	dispatcher := jobs.FleetWide(compose.TelegramPollSweepArgs{})

	// river_job is not workspace-scoped and River assigns its ids, so the batch
	// is fenced by the table's high-water mark rather than by a workspace tx or
	// a truncate: another suite's leftover rows in this process would otherwise
	// answer a query keyed on kind alone.
	var highWater int64
	if err := e.Pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM river_job`).Scan(&highWater); err != nil {
		t.Fatalf("reading the river_job high-water mark: %v", err)
	}

	for _, args := range tenantJobs {
		if err := inserter.Enqueue(ctx, args, nil); err != nil {
			t.Fatalf("enqueueing %s: %v", args.Kind(), err)
		}
	}
	if err := inserter.Enqueue(ctx, dispatcher, nil); err != nil {
		t.Fatalf("enqueueing %s: %v", dispatcher.Kind(), err)
	}

	onWire := workspaceKeysOnTheWire(t, e, highWater)
	if len(onWire) != len(tenantJobs)+1 {
		t.Fatalf("river_job holds %d of this batch's kinds, want %d — an insert that never landed would make every assertion below vacuous", len(onWire), len(tenantJobs)+1)
	}

	for _, args := range tenantJobs {
		got, present := onWire[args.Kind()]
		if !present {
			t.Errorf("%s: no row landed for this kind", args.Kind())
			continue
		}
		if got == nil {
			t.Errorf("%s: args->>'workspace_id' is null — the tenant this job was enqueued for (%s) is invisible to every per-workspace read of river_job, which reads the null as a dispatcher rather than as work it cannot see", args.Kind(), args.WorkspaceID())
			continue
		}
		if want := args.WorkspaceID().String(); *got != want {
			t.Errorf("%s: args->>'workspace_id' = %q, want %q — the wire carries a different tenant than the args the job was enqueued with", args.Kind(), *got, want)
		}
	}

	if got, present := onWire[dispatcher.Kind()]; !present {
		t.Errorf("%s: no row landed for the dispatcher, so the null half of the invariant went unchecked", dispatcher.Kind())
	} else if got != nil {
		t.Errorf("%s: args->>'workspace_id' = %q, want null — a dispatcher enumerates the fleet and does no tenant work, so a workspace on its row is counted as a pass some tenant never got", dispatcher.Kind(), *got)
	}
}
