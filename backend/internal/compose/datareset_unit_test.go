// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
		pool:             nil,
		seeds:            deployconfig.Seeds{},
		dataResetAllowed: true,
		log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
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
	if !s.dataResetAllowed {
		t.Fatal("WithDataReset did not carry the armed switch to the handler")
	}
}

// TestResetDataRefusesUnlessArmed drives the handler itself, not the wiring.
//
// The switch is checked BEFORE any auth, so an installation that did not arm
// the reset answers 404 to everyone — the endpoint does not exist there, and a
// caller cannot learn it might. That ordering is what the second case pins: the
// same unauthenticated request against an ARMED installation gets an auth
// refusal instead, which is only reachable once the switch has let it past.
func TestResetDataRefusesUnlessArmed(t *testing.T) {
	// A non-nil pool so the refusal is attributable to the switch alone; it is
	// never dialed, because the gate returns before any query.
	armedLike := func(allowed bool) dataResetHandlers {
		return dataResetHandlers{
			pool:             &pgxpool.Pool{},
			dataResetAllowed: allowed,
			log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	}

	unarmed := httptest.NewRecorder()
	armedLike(false).ResetData(unarmed, httptest.NewRequest(http.MethodPost, "/v1/admin/reset-data", nil))
	if unarmed.Code != http.StatusNotFound {
		t.Fatalf("an installation that did not arm the reset answered %d, want 404", unarmed.Code)
	}

	armed := httptest.NewRecorder()
	armedLike(true).ResetData(armed, httptest.NewRequest(http.MethodPost, "/v1/admin/reset-data", nil))
	if armed.Code == http.StatusNotFound {
		t.Fatal("an armed installation still answered 404, so the switch gates nothing")
	}
}
