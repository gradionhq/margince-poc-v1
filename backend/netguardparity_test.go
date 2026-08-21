// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package backendarch

// The egress SSRF denylist exists twice, and this is where the two are held
// equal. internal/platform/netguard owns the guard the core dials through;
// pkg/extension PUBLISHES the same ranges, because an extension unit is its own
// module and may import only the published pkg/** surface.
//
// It used to be published nowhere, so a unit that dials a member-supplied host
// hand-copied the literals and this test read that unit's file off disk. That
// worked only while the unit lived in this repository — and the drift it guarded
// against was not a style difference but the way around the core's guard: a
// range the core refuses and the copy admits is an internal address a member can
// name. Publishing the list removes the class of bug instead of re-testing it,
// and what remains to guard is these two lists inside this one module.
//
// It compares the parsed networks rather than file text: the two are formatted
// and grouped differently on purpose.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/netguard"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// The published list and the guard's own list are the same list. A unit that
// reads it from the surface cannot drift from the guard; a unit that hand-copied
// it could, and that drift was the way around the guard for a member-supplied
// host.
func TestPublishedReservedNetsMatchTheGuard(t *testing.T) {
	published := extension.ReservedNets()
	internal := netguard.ReservedNetsForTest()
	if len(published) != len(internal) {
		t.Fatalf("published %d nets, guard has %d", len(published), len(internal))
	}
	for i := range published {
		if published[i].String() != internal[i].String() {
			t.Errorf("net %d: published %s, guard %s", i, published[i], internal[i])
		}
	}
}

// The published slice must not be the guard's own backing array. It is returned
// across a module boundary to callers the core does not control, and a caller
// that reslices or overwrites an element would be editing the guard itself.
func TestReservedNetsDoesNotHandOutTheGuardsOwnSlice(t *testing.T) {
	first := extension.ReservedNets()
	if len(first) == 0 {
		t.Fatal("ReservedNets returned nothing — the denylist is the guard")
	}
	first[0] = nil
	if extension.ReservedNets()[0] == nil {
		t.Error("a caller mutating the returned slice changed what the next caller sees")
	}
}
