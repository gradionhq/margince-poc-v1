// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// /me reports the deployment posture so the client can hide the destructive
// admin "Reset data" action outside a non-production role (task 7). This
// proves the posture lands in the response and that the zero value (no
// posture wired) degrades to production — never accidentally exposing the
// action.

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestMeResponseCarriesNonProduction(t *testing.T) {
	id := Identity{Roles: []string{"admin"}}
	if got := meResponse(id, crmcontracts.Native, true); !got.NonProduction {
		t.Fatal("want NonProduction true")
	}
	if meResponse(id, crmcontracts.Native, false).NonProduction {
		t.Fatal("want NonProduction false")
	}
}

func TestHandlersWithNonProductionDefaultsFalse(t *testing.T) {
	var h Handlers
	if h.nonProduction {
		t.Fatal("zero-value Handlers must default to production (nonProduction=false)")
	}
	h = h.WithNonProduction(true)
	if !h.nonProduction {
		t.Fatal("WithNonProduction(true) did not set the field")
	}
}
