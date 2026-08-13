// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package events

// The extension tier's half of the bus contract: a unit-authored type routes to
// the one extension stream, the two vocabularies cannot shadow each other, and
// no core consumer group carries the tier's traffic.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestExtensionTypesRouteToTheExtensionStream(t *testing.T) {
	for _, typ := range []string{"ext_notes.note_added", "ext_de.retention_recomputed", "ext_a1.x"} {
		stream, err := StreamFor(typ)
		if err != nil {
			t.Errorf("StreamFor(%q) refused a well-formed extension type: %v", typ, err)
			continue
		}
		if stream != "gw:events:crm:extension" {
			t.Errorf("StreamFor(%q) = %q, want the extension stream", typ, stream)
		}
		if got := VersionOf(typ); got != ExtensionEventVersion {
			t.Errorf("VersionOf(%q) = %d, want %d — a unit names a new verb rather than a version", typ, got, ExtensionEventVersion)
		}
	}
}

func TestMalformedExtensionTypesAreUnroutable(t *testing.T) {
	// Each of these is a way a type could ALMOST look like a unit's and must
	// not route: an unroutable outbox row wedges the relay, so the refusal
	// belongs at the publisher.
	for _, typ := range map[string]string{
		"ext_notes.NoteAdded": "an upper-case verb",
		"ext_notes.":          "no verb at all",
		"ext_notes":           "no verb segment",
		"extnotes.added":      "no ext_ namespace prefix",
		"ext_.added":          "an empty namespace",
		"ext_notes.2nd_try":   "a verb starting with a digit",
		"notes.added":         "a bare unit name, which would read as a core family",
	} {
		if _, err := StreamFor(typ); err == nil {
			t.Errorf("StreamFor(%q) routed a malformed extension type", typ)
		}
		if IsExtensionType(typ) {
			t.Errorf("IsExtensionType(%q) accepted a malformed extension type", typ)
		}
	}
}

// The two vocabularies are held apart at their source. If a catalog type ever
// matched the extension grammar, StreamFor's catalog-first ordering would be
// the only thing keeping it on its own stream — and a publisher naming it
// would be indistinguishable from a unit.
func TestNoCatalogTypeLooksLikeAnExtensionType(t *testing.T) {
	types := Types()
	if len(types) == 0 {
		t.Fatal("the catalog is empty; this test would pass over nothing")
	}
	for _, typ := range types {
		if IsExtensionType(typ) {
			t.Errorf("catalog type %q matches the extension grammar — a core family may not be spelled ext_<ns>.<verb>", typ)
		}
	}
}

// A unit's event is delivered to the units that ASKED for it. A core group
// carrying the extension stream would wake the automation engine and the
// webhook fan-out for every unit event, match nothing, and cost a database
// round trip each — invisibly, and growing with the tier.
func TestNoCoreGroupCarriesTheExtensionStream(t *testing.T) {
	extension := ExtensionStream()
	// Asserted first, so the absence below cannot pass merely because the
	// stream does not exist.
	var published bool
	for _, s := range Streams() {
		if s == extension {
			published = true
		}
	}
	if !published {
		t.Fatalf("Streams() does not carry %q, so nothing purges or measures it", extension)
	}

	groups := Groups()
	if len(groups) == 0 {
		t.Fatal("Groups() is empty; this test would pass over nothing")
	}
	for _, g := range groups {
		for _, s := range g.Streams {
			if s == extension {
				t.Errorf("core group %s subscribes to %q", g.Name, extension)
			}
		}
	}
}

func TestExtensionEnvelopeValidates(t *testing.T) {
	env := Envelope{
		EventID:    ids.NewV7(),
		Type:       "ext_notes.filing_withdrawn",
		Version:    ExtensionEventVersion,
		OccurredAt: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
		Actor:      Actor{Type: "system", ID: "system"},
		// The subject is the unit's OWN row, named by the unit's own table.
		Entity:  EntityRef{Type: "ext_notes_note", ID: ids.NewV7()},
		Payload: json.RawMessage(`{"activity_id":"1e2d"}`),
		Trace:   Trace{CorrelationID: ids.NewV7(), AuditLogID: ids.NewV7()},
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("a complete extension envelope was refused: %v", err)
	}

	// The version is stamped from VersionOf, never written at the port; an
	// envelope claiming another one is a publisher that stopped agreeing with
	// the bus contract.
	skewed := env
	skewed.Version = ExtensionEventVersion + 1
	if err := skewed.Validate(); err == nil {
		t.Error("Validate accepted an extension envelope at a version the tier does not have")
	}

	// An extension event is not a pipeline event: it names its subject, or it
	// is a fact no consumer can read back.
	subjectless := env
	subjectless.Entity = EntityRef{}
	if err := subjectless.Validate(); err == nil {
		t.Error("Validate accepted an extension envelope with no entity ref")
	}
}
