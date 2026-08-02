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
