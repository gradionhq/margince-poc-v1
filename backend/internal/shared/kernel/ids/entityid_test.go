// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ids

import (
	"encoding/json"
	"testing"
)

func TestTypedIDRoundTrips(t *testing.T) {
	p := New[PersonKind]()
	if p.IsZero() {
		t.Fatal("New minted a zero id")
	}
	if p.EntityType() != "person" {
		t.Fatalf("EntityType = %q, want person", p.EntityType())
	}
	if p.Ref() != (Ref{Type: "person", ID: p.UUID}) {
		t.Fatalf("Ref = %+v", p.Ref())
	}

	parsed, err := ParseAs[PersonKind](p.String())
	if err != nil || parsed != p {
		t.Fatalf("ParseAs(%s) = %v (%v)", p, parsed, err)
	}

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back PersonID
	if err := json.Unmarshal(raw, &back); err != nil || back != p {
		t.Fatalf("json round-trip = %v (%v)", back, err)
	}

	var scanned PersonID
	if err := scanned.Scan(p.String()); err != nil || scanned != p {
		t.Fatalf("Scan(string) = %v (%v)", scanned, err)
	}
	if err := scanned.Scan(p.UUID[:]); err != nil || scanned != p {
		t.Fatalf("Scan([16]byte) = %v (%v)", scanned, err)
	}
	v, err := p.Value()
	if err != nil || v != p.String() {
		t.Fatalf("Value = %v (%v)", v, err)
	}

	// Map keys and From: the escape hatch stays explicit and typed.
	m := map[PersonID]int{p: 1}
	if m[From[PersonKind](p.UUID)] != 1 {
		t.Fatal("From did not reproduce the same key")
	}
}
