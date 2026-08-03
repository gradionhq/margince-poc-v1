// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The reset-data HTTP transport's gate chain over real migrated Postgres:
// environment (production ⇒ 404 even for an admin) → human-only → admin-only
// → typed confirmation → success. The handler is exercised directly (not
// through the full router) since this test owns the gate chain, not the
// auth middleware that binds principals onto a request.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

func TestResetDataEndpointGates(t *testing.T) {
	e := integration.Setup(t)
	e.SeedPerson(t, "Alice", nil)

	call := func(ctx context.Context, env runtimeenv.Environment, body string) *httptest.ResponseRecorder {
		h := dataResetHandlers{
			pool: e.Pool, schemaPool: nil, seeds: deployconfig.Seeds{}, env: env,
			log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/reset-data", strings.NewReader(body)).WithContext(ctx)
		rec := httptest.NewRecorder()
		h.ResetData(rec, req)
		return rec
	}

	if rec := call(e.Admin(), runtimeenv.Production, `{"confirmation":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("production posture: got %d, want 404", rec.Code)
	}

	if rec := call(e.AgentCtx(), runtimeenv.Development, `{"confirmation":"x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("agent principal: got %d, want 403", rec.Code)
	}

	nonAdmin := e.As(ids.NewV7(), nil, integration.RepPerms)
	if rec := call(nonAdmin, runtimeenv.Development, `{"confirmation":"x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin human: got %d, want 403", rec.Code)
	}

	if rec := call(e.Admin(), runtimeenv.Development, `{"confirmation":"wrong"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong confirmation: got %d, want 422", rec.Code)
	}

	rec := call(e.Admin(), runtimeenv.Development, `{"confirmation":"Authz"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("happy path: got %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Status        string `json:"status"`
		TablesCleared int    `json:"tables_cleared"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	if out.Status != "reset" {
		t.Fatalf("status = %q, want %q", out.Status, "reset")
	}
	if out.TablesCleared <= 0 {
		t.Fatalf("tables_cleared = %d, want > 0", out.TablesCleared)
	}
}
