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

// TestResetDataOverHTTP drives the armed admin reset end to end over the real
// router and a real admin session (what the direct-handler test cannot see):
// the WithDataReset / WithDataResetAvailable wiring, the data_reset_available
// flag /me carries for the client gate, the confirmation refusal, and a
// successful reset. Reaching 200 also proves the live session path populates
// the admin RoleKeys that RequireAdmin gates on.
//
// It runs under the PRODUCTION posture on purpose. Armed-and-non-production is
// the one combination the retired gate would also have served, so a test using
// it proves nothing about which gate is live; armed-and-production can only
// pass if the capability is independent of the posture, and fails against any
// regression to `allowed && env.IsNonProduction()`.
func TestResetDataOverHTTP(t *testing.T) {
	e := apptest.SetupAppWithOptions(t,
		compose.WithDataReset(nil, deployconfig.Seeds{}, true),
		compose.WithNonProduction(runtimeenv.Production),
		compose.WithDataResetAvailable(true),
	)
	apptest.BootstrapWorkspaceSession(t, e, "Fable E2E", "ada@example.com", "Ada Admin")

	var me struct {
		NonProduction      bool  `json:"non_production"`
		DataResetAvailable *bool `json:"data_reset_available"`
	}
	if code := e.Call(t, "GET", "/v1/me", nil, nil, &me); code != 200 {
		t.Fatalf("GET /me = %d, want 200", code)
	}
	// The field the SPA gates on, over the real router. Asserting only the
	// posture would leave a dropped WithDataResetAvailable green here while
	// every client's button disappeared.
	if me.DataResetAvailable == nil || !*me.DataResetAvailable {
		t.Fatalf("me.data_reset_available = %v; the endpoint below is armed, so the client must be told so", me.DataResetAvailable)
	}
	// And the posture travels separately, reporting production — which is the
	// point: the two answers disagree here, and the reset below still runs.
	if me.NonProduction {
		t.Fatal("me.non_production = true under the Production posture")
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
