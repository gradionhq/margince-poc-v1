// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Scopes a worker binds before it calls an engine — the counterpart to
// harness.go's As, which builds the scope a HUMAN request arrives with. Separate
// from it because a suite reaching for one of these is asking a different
// question: not "what may this caller see" but "what provenance does the row this
// pass writes carry".

// RetentionPassCtx is the scope the retention workspace worker binds before it
// calls the engine: the tenant, the system actor, and a fresh correlation id.
// The engine writes an audit row and an outbox event per record it retires, so a
// suite that bound only the workspace would be exercising a pass whose provenance
// production never has.
//
// Exported because a sibling suite package (integration/jobfanout) binds the same
// scope. The actor is a SYSTEM principal, which no row-scope clause narrows — so
// this builds the scope a PASS runs under, and a suite asserting who may SEE a
// row wants Env.As instead, which can be denied.
func RetentionPassCtx(ws ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}
