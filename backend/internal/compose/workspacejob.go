// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The scope a workspace-scoped job runs under. River's WorkerMiddleware sees
// a rivertype.JobRow — raw JSON, never the typed args — so a middleware could
// only bind by re-reading the wire key, which would leave the role declaration
// a label beside the binding rather than the thing that governs it. Binding
// from the args' own WorkspaceID() instead keeps the declaration load-bearing:
// a worker cannot claim one workspace and work in another.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/jobs"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// workspaceJobCtx binds the workspace the args themselves declare, and only
// that. Provenance stays where it already is — each pass names its own actor
// and mints its own correlation id, and moving that here would change what the
// audit rows say about work whose behaviour is meant to be untouched.
//
// A zero id is REFUSED rather than bound. An unbound GUC does not fail here;
// it fails at the first tenant query, somewhere far less legible, and only
// after the job has already begun. It is also what an args type decodes to
// when a queued job predates a change to its wire key, so the refusal is the
// difference between a loud failure and a pass that quietly touches nothing.
func workspaceJobCtx(ctx context.Context, args jobs.WorkspaceScoped) (context.Context, error) {
	ws := args.WorkspaceID()
	if ws == (ids.UUID{}) {
		return nil, fmt.Errorf("%s: declares WorkspaceScoped but carries no workspace", args.Kind())
	}
	return principal.WithWorkspaceID(ctx, ws), nil
}

// installationJobCtx binds the installation's workspace for a collapsed pass —
// one that carries no workspace of its own (ADR-0103 §1).
//
// The binding is NOT vestigial while any store still stamps its workspace_id
// column from the context: storekit.MustWorkspace reads it, sixteen deals sites
// and a handful in people, capture, ai and activities call it, and an agent tool
// reaches several of them. Unbound, those inserts would stamp a zero uuid and
// fail their foreign key — loudly, but for a reason nobody would recognise. It
// retires with MustWorkspace itself (ADR-0091 §5), not before.
func installationJobCtx(ctx context.Context, svc *identity.Service) (context.Context, error) {
	ws, err := svc.InstallationWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	return principal.WithWorkspaceID(ctx, ws.UUID), nil
}

// workspaceJobDB binds a handle to the workspace a per-workspace job NAMES.
//
// A fleet pass runs one job per workspace, so the installation resolver is the
// wrong answer here twice over: it refuses outright while more than one
// workspace exists (ADR-0061 §3), and even with one it would be answering a
// question the job has already answered in its args. The workspace comes from
// the args exactly as it did when the binding rode the context — this is the
// same fact, moved to where the transaction now reads it (ADR-0091 §9 step 3).
func workspaceJobDB(pool *pgxpool.Pool, args jobs.WorkspaceScoped) (*database.DB, error) {
	ws := args.WorkspaceID()
	if ws == (ids.UUID{}) {
		return nil, fmt.Errorf("%s: declares WorkspaceScoped but carries no workspace", args.Kind())
	}
	return database.BindTo(pool, ids.From[ids.WorkspaceKind](ws)), nil
}
