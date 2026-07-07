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

// TestEveryEntityKindReportsItsDiscriminator pins the closed vocabulary:
// each entity's EntityType() must be the exact string the polymorphic
// seams (activity links, list membership, audit/event envelopes) carry.
// A kind added to entityid.go without its discriminator string here is a
// silent hole in every polymorphic write — so every kind is exercised.
func TestEveryEntityKindReportsItsDiscriminator(t *testing.T) {
	got := map[string]string{
		"workspace":       New[WorkspaceKind]().EntityType(),
		"user":            New[UserKind]().EntityType(),
		"team":            New[TeamKind]().EntityType(),
		"person":          New[PersonKind]().EntityType(),
		"organization":    New[OrganizationKind]().EntityType(),
		"lead":            New[LeadKind]().EntityType(),
		"deal":            New[DealKind]().EntityType(),
		"pipeline":        New[PipelineKind]().EntityType(),
		"stage":           New[StageKind]().EntityType(),
		"offer":           New[OfferKind]().EntityType(),
		"product":         New[ProductKind]().EntityType(),
		"activity":        New[ActivityKind]().EntityType(),
		"signal":          New[SignalKind]().EntityType(),
		"list":            New[ListKind]().EntityType(),
		"tag":             New[TagKind]().EntityType(),
		"saved_view":      New[SavedViewKind]().EntityType(),
		"approval":        New[ApprovalKind]().EntityType(),
		"automation":      New[AutomationKind]().EntityType(),
		"passport":        New[PassportKind]().EntityType(),
		"consent_purpose": New[PurposeKind]().EntityType(),
	}
	for want, have := range got {
		if have != want {
			t.Errorf("EntityType = %q, want %q", have, want)
		}
	}
}

// TestScanAndParseRejectBadInput exercises the error and byte-string arms
// of the Scan/ParseAs seams — the ones that turn corrupt DB or wire bytes
// into an honest error instead of a silently-zero id.
func TestScanAndParseRejectBadInput(t *testing.T) {
	p := New[PersonKind]()

	// []byte carrying the canonical string form (not a raw 16-byte value).
	var fromText PersonID
	if err := fromText.Scan([]byte(p.String())); err != nil || fromText != p {
		t.Fatalf("Scan([]byte text) = %v (%v)", fromText, err)
	}

	var bad PersonID
	if err := bad.Scan(1234); err == nil {
		t.Fatal("Scan(int) should reject an unsupported source type")
	}
	if err := bad.Scan("not-a-uuid"); err == nil {
		t.Fatal("Scan(bad string) should reject a malformed uuid")
	}
	if err := bad.Scan([]byte("not-a-valid-uuid-text-value")); err == nil {
		t.Fatal("Scan(bad []byte) should reject a malformed uuid")
	}

	if _, err := ParseAs[PersonKind]("not-a-uuid"); err == nil {
		t.Fatal("ParseAs(malformed) should error")
	}
}
