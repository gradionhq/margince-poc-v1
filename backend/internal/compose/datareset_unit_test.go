// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
)

// TestResetRunRequiresBoundWorkspace: run() refuses before it dials the pool
// when no workspace is bound to the context — the fail-closed guard for a
// caller that somehow reached run() outside the admission chain that always
// binds one. Pool is nil precisely to prove the workspace check returns first.
func TestResetRunRequiresBoundWorkspace(t *testing.T) {
	h := dataResetHandlers{
		pool:    nil,
		seeds:   deployconfig.Seeds{},
		allowed: true,
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if _, err := h.run(context.Background(), "irrelevant"); !errors.Is(err, database.ErrNoWorkspace) {
		t.Fatalf("run without a bound workspace: got %v, want ErrNoWorkspace", err)
	}
}

// TestWithDataResetWiresTheComposedPool proves the option takes the app-role
// pool the composition hands every option (the WithSchemaPool contract), not a
// separately captured one, and threads the switch through — the wiring the
// production cmd/api path depends on.
func TestWithDataResetWiresTheComposedPool(t *testing.T) {
	var s Server
	composed := &pgxpool.Pool{} // never dialed; identity comparison only
	WithDataReset(nil, deployconfig.Seeds{}, true)(&s, composed)
	if s.dataResetHandlers.pool != composed {
		t.Fatal("WithDataReset did not wire the composed app-role pool")
	}
	if !s.dataResetHandlers.allowed {
		t.Fatal("WithDataReset did not carry the armed switch to the handler")
	}
}

// The switch is what the endpoint answers on, so an installation that did not
// arm it holds a handler that refuses — with the pool wired, which is what makes
// this about the flag rather than about missing wiring.
func TestWithDataResetUnarmedIsClosed(t *testing.T) {
	var s Server
	WithDataReset(nil, deployconfig.Seeds{}, false)(&s, &pgxpool.Pool{})
	if s.dataResetHandlers.allowed {
		t.Fatal("an installation that did not arm the reset got an armed handler")
	}
}
