// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package overlay

// Connect reaches the incumbent twice before it answers — for the portal id the
// webhook binding needs, and for the owners directory mirror_user_map is seeded
// from — and both are best-effort by design: an admin who has just pasted a
// valid token must not be told the connection failed because a vendor was slow.
//
// That tolerance had no test. It was covered by accident: the lane dialled
// api.hubapi.com for real with a fixture token, HubSpot answered 401, and the
// swallow path ran on every single connect in the suite (#1996). Removing the
// dial removed the coverage with it, so this states the property deliberately —
// and states it over the incumbent the composed server binds ITSELF, with no
// resolver override, which is the one arrangement no other test in this package
// exercises.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/platform/keyvault"
)

func TestConnectStandsUpAnOverlayTheIncumbentWouldNotAnswer(t *testing.T) {
	e := apptest.SetupAppWithOptions(t, compose.WithKeyvault(keyvault.NewMemory()))
	e.BootstrapWorkspace(t)

	var conn apptest.AnyMap
	if status := e.Call(t, "POST", "/v1/overlay/connection", apptest.AnyMap{
		"incumbent": "hubspot", "region": "eu1", "privateAppToken": "fake-token-never-used",
	}, nil, &conn); status != http.StatusCreated {
		t.Fatalf("connect overlay = %d %v — a connect must survive an incumbent that will not answer", status, conn)
	}

	ctx := context.Background()
	// NULL, not the empty string. The column is what a later webhook matches a
	// portal against, and "" would match a payload carrying no portal at all —
	// which is why insertConnection writes NULLIF($5, '') rather than the raw id.
	var accountID *string
	if err := e.Owner.QueryRow(ctx,
		`SELECT incumbent_account_id FROM incumbent_connection`).Scan(&accountID); err != nil {
		t.Fatalf("reading the stored account id: %v", err)
	}
	if accountID != nil {
		t.Fatalf("incumbent_account_id = %q, want NULL — the portal fetch did not answer, so there is nothing to bind yet", *accountID)
	}

	// And the second best-effort call: no owners directory means no mappings,
	// not a half-seeded one.
	var mappings int
	if err := e.Owner.QueryRow(ctx, `SELECT count(*) FROM mirror_user_map`).Scan(&mappings); err != nil {
		t.Fatalf("counting the seeded user map: %v", err)
	}
	if mappings != 0 {
		t.Fatalf("mirror_user_map holds %d row(s) after a connect whose owners fetch failed, want none", mappings)
	}
}
