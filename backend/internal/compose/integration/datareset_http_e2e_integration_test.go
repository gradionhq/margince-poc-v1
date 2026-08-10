// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// TestResetDataOverHTTP drives the non-production admin reset end to end over
// the real router and a real admin session (what the direct-handler test
// cannot see): the WithDataReset / WithNonProduction wiring, the non_production
// posture /me carries for the client gate, the confirmation refusal, and a
// successful reset. Reaching 200 also proves the live session path populates
// the admin RoleKeys that RequireAdmin gates on.
func TestResetDataOverHTTP(t *testing.T) {
	e := apptest.SetupAppWithOptions(t,
		compose.WithDataReset(nil, deployconfig.Seeds{}, runtimeenv.Development),
		compose.WithNonProduction(runtimeenv.Development),
	)
	apptest.BootstrapWorkspaceSession(t, e, "Fable E2E", "ada@example.com", "Ada Admin")

	var me struct {
		NonProduction bool `json:"non_production"`
	}
	if code := e.Call(t, "GET", "/v1/me", nil, nil, &me); code != 200 {
		t.Fatalf("GET /me = %d, want 200", code)
	}
	if !me.NonProduction {
		t.Fatal("me.non_production = false; want true under the Development posture")
	}

	// Wrong confirmation is refused before anything is deleted.
	if code := e.Call(t, "POST", "/v1/admin/reset-data", apptest.AnyMap{"confirmation": "wrong"}, nil, nil); code != 422 {
		t.Fatalf("reset with wrong confirmation = %d, want 422", code)
	}

	// The organization name resets the workspace to first-boot state.
	var out struct {
		Status        string `json:"status"`
		TablesCleared int    `json:"tables_cleared"`
	}
	if code := e.Call(t, "POST", "/v1/admin/reset-data", apptest.AnyMap{"confirmation": "Fable E2E"}, nil, &out); code != 200 {
		t.Fatalf("reset with the org name = %d, want 200", code)
	}
	if out.Status != "reset" {
		t.Fatalf("reset status = %q, want %q", out.Status, "reset")
	}
	if out.TablesCleared == 0 {
		t.Fatal("reset reported 0 tables cleared; the catalog-derived sweep set is never empty")
	}
}
