// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

import (
	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// WorkspaceScoped is implemented by job args whose work belongs to exactly
// one workspace. The job row IS that workspace's pass: it succeeds or fails
// on its own, is retried on its own, and reports its own failure — none of
// which is true of a pass that loops the fleet inside one row.
//
// The accessor is WorkspaceID rather than a bare field because Go forbids a
// method and a field of the same name, so implementations hold the value in a
// `Workspace ids.UUID` field. The WIRE key is whatever that kind already
// shipped with: a kind enqueued exactly once (a staged send, a backfill run, a
// raw capture's ingest) has rows sitting in the queue across a deploy, and
// renaming its key would decode them to a zero workspace that the binding then
// refuses on every attempt — stranding the domain row those jobs exist to
// advance. New kinds use `workspace_id`.
type WorkspaceScoped interface {
	river.JobArgs
	WorkspaceID() ids.UUID
}

// FleetWide is implemented by DISPATCHER args: a job that enumerates the
// fleet and enqueues one WorkspaceScoped job per workspace. A dispatcher
// may read to discover work; it does no tenant WRITE, because the write is
// the workspace job's to make and to be judged on.
//
// ONE kind carries this marker while still writing: embed_reindex re-embeds
// every workspace's corpus inside a single row. It is not a dispatcher and is
// not pretending to be — its marker claim is fleet-wide and single-flight, so
// its fan-out is not the common recipe, and it is recorded as deferred rather
// than cleared. It binds each workspace before writing, so the writes are
// scoped; what it does not have is a row per workspace to fail as. Do not read
// it as precedent: a new fleet-wide worker that writes is the shape this
// interface exists to name.
//
// The marker method is empty on purpose — it is a declaration, not
// behaviour, and it is what the G1 gate reads.
type FleetWide interface {
	river.JobArgs
	FleetWide()
}
