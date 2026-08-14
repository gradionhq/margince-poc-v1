// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import (
	"strings"
	"testing"
)

// TestEveryClassSharingACanonicalTargetIsSeparableByItsMirrorNamespace holds
// the module to the premise re-projection rests on: the mirror is keyed by the
// CANONICAL class, so every declaration that projects onto one target puts its
// rows in the same bucket, while every re-read of a record names the INCUMBENT
// class. A row can only be re-fetched under the class that produced it, and the
// mirror id's "<class>:" namespace (OVA-MAP-7) is what tells the rows apart.
//
// The ids are built through the identity bridge rather than concatenated here,
// because the bridge is what a namespace change would have to survive: it is
// the same reversal a force-fresh recovers a class with, so a namespace this
// filter could not reproduce would be a mirror key nothing could resolve.
func TestEveryClassSharingACanonicalTargetIsSeparableByItsMirrorNamespace(t *testing.T) {
	for _, class := range incumbentEngagementClasses {
		id, err := externalIDToUUID(class + ":123")
		if err != nil {
			t.Fatalf("externalIDToUUID(%q): %v", class+":123", err)
		}
		mirrorID := uuidToExternalID(id)
		if prefix := mirrorIDNamespace(class); !strings.HasPrefix(mirrorID, prefix) || prefix == "" {
			t.Errorf("mirrorIDNamespace(%q) = %q, which does not namespace that class's own mirror id %q — "+
				"the sweep would re-project rows no declaration of %q produced, or none of them", class, prefix, mirrorID, class)
		}
		for _, sibling := range incumbentEngagementClasses {
			if sibling != class && strings.HasPrefix(mirrorID, mirrorIDNamespace(sibling)) {
				t.Errorf("%q's mirror id %q also carries %q's namespace, so a re-fetch would read a %s as a %s: "+
					"a live incumbent read that can only 404, repeated every pass", class, mirrorID, sibling, class, sibling)
			}
		}
	}
	// A class that owns its canonical target shares the bucket with nobody, so
	// it narrows nothing: the empty prefix every id starts with.
	if prefix := mirrorIDNamespace(IncumbentClassContacts); prefix != "" {
		t.Errorf("mirrorIDNamespace(%q) = %q, want the empty prefix — a class with its target to itself "+
			"must select every row of it, and its ids are bare", IncumbentClassContacts, prefix)
	}
}
