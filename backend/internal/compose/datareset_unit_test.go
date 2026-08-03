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
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// TestResetRunRequiresBoundWorkspace: run() refuses before it dials the pool
// when no workspace is bound to the context — the fail-closed guard for a
// caller that somehow reached run() outside the admission chain that always
// binds one. Pool is nil precisely to prove the workspace check returns first.
func TestResetRunRequiresBoundWorkspace(t *testing.T) {
	h := dataResetHandlers{
		pool:  nil,
		seeds: deployconfig.Seeds{},
		env:   runtimeenv.Development,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if _, err := h.run(context.Background(), "irrelevant"); !errors.Is(err, database.ErrNoWorkspace) {
		t.Fatalf("run without a bound workspace: got %v, want ErrNoWorkspace", err)
	}
}

// TestWithDataResetWiresTheComposedPool proves the option takes the app-role
// pool the composition hands every option (the WithSchemaPool contract), not a
// separately captured one, and threads the posture through — the wiring the
// production cmd/api path depends on.
func TestWithDataResetWiresTheComposedPool(t *testing.T) {
	var s Server
	composed := &pgxpool.Pool{} // never dialed; identity comparison only
	WithDataReset(nil, deployconfig.Seeds{}, runtimeenv.Staging)(&s, composed)
	if s.dataResetHandlers.pool != composed {
		t.Fatal("WithDataReset did not wire the composed app-role pool")
	}
	if s.env != runtimeenv.Staging {
		t.Fatalf("WithDataReset env = %q, want staging", s.env)
	}
}
