// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"context"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RetentionPassCtx is the scope the retention workspace worker binds before it
// calls the engine: the tenant, the system actor, and a fresh correlation id.
// The engine writes an audit row and an outbox event per record it retires, so a
// suite that bound only the workspace would be exercising a pass whose provenance
// production never has. It is the counterpart to harness.go's As, which builds the
// scope a HUMAN request arrives with.
//
// The actor is a SYSTEM principal, which no row-scope clause narrows, so this
// scope cannot be denied — which makes it the wrong instrument for any assertion
// about who may SEE a row. Such a test would pass whatever the row scope was. That
// is not left to this comment: backend/retentionscope_test.go holds the fixture to
// the retention engine as its only sink, and fails a use that is anything else.
func RetentionPassCtx(ws ids.UUID) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), ws)
	ctx = principal.WithActor(ctx, principal.Principal{Type: principal.PrincipalSystem, ID: "system"})
	return principal.WithCorrelationID(ctx, ids.NewV7())
}
