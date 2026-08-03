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
// `Workspace ids.UUID` field, wired as `json:"workspace_id"` — one spelling on
// every kind, held there by
// TestEveryWorkspaceScopedArgsSpellsItsWorkspaceKeyTheSameWay. A kind that
// spelled it differently would be invisible to `args->>'workspace_id'`, and a
// per-workspace read of river_job would report it as no work at all rather
// than as work the query cannot see.
//
// That read is exact in both directions: a null in that column means a
// dispatcher, because a job that does tenant work declares its workspace.
type WorkspaceScoped interface {
	river.JobArgs
	WorkspaceID() ids.UUID
}

// FleetWide is implemented by DISPATCHER args: a job that enumerates the
// fleet and enqueues one WorkspaceScoped job per workspace. A dispatcher
// may read to discover work; it does no tenant WRITE, because the write is
// the workspace job's to make and to be judged on.
//
// The marker method is empty on purpose — it is a declaration, not
// behaviour, and it is what the G1 gate reads.
type FleetWide interface {
	river.JobArgs
	FleetWide()
}
