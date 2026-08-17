// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// /me carries two facts that used to be one, and the split is the point: the
// deployment POSTURE, and whether the destructive reset is ARMED. A deployment
// being non-production is not consent to purge its tenant data.
//
// Both degrade closed when unwired, which is what keeps a role that forgot the
// option from offering an action the server would refuse.

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestMeResponseCarriesNonProduction(t *testing.T) {
	id := Identity{Roles: []string{"admin"}}
	if got := NewHandlers(&Service{}).WithNonProduction(true).meResponse(id, crmcontracts.Native); !got.NonProduction {
		t.Fatal("want NonProduction true")
	}
	if NewHandlers(&Service{}).meResponse(id, crmcontracts.Native).NonProduction {
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

func TestMeResponseCarriesTheDataResetSwitchSeparately(t *testing.T) {
	id := Identity{Roles: []string{"admin"}}
	// Non-production ALONE must not offer the action. This is the case that used
	// to be conflated, and the one a staging installation lived in.
	posture := NewHandlers(&Service{}).WithNonProduction(true).meResponse(id, crmcontracts.Native)
	if posture.DataResetAvailable == nil || *posture.DataResetAvailable {
		t.Fatal("a non-production installation that never armed the reset was offered it anyway")
	}
	armed := NewHandlers(&Service{}).WithDataResetAvailable(true).meResponse(id, crmcontracts.Native)
	if armed.DataResetAvailable == nil || !*armed.DataResetAvailable {
		t.Fatal("an armed installation was not offered the reset")
	}
	// And arming it says nothing about the posture: the two travel separately.
	if armed.NonProduction {
		t.Fatal("arming the reset changed the reported deployment posture")
	}
}

func TestHandlersDefaultToNoDataReset(t *testing.T) {
	var h Handlers
	if h.dataResetAvailable {
		t.Fatal("zero-value Handlers offered the destructive reset; the default must be closed")
	}
	if !h.WithDataResetAvailable(true).dataResetAvailable {
		t.Fatal("WithDataResetAvailable(true) did not set the field")
	}
}
