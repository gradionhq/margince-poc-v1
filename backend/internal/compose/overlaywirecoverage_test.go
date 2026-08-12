// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A binding that claims the mirror carries a field is a claim about what a
// CALLER RECEIVES, not about what the ingest layer stored. The two came apart
// once already — address and owner_id were mapped into the mirror and picked
// up by nothing — and the distance was invisible because every layer was
// correct on its own: the mapping landed the value, the mirror held it, and
// the wire assembly simply never named it. This gate closes that distance by
// asserting behaviour rather than declarations: a record the real mapping
// pipeline produced must yield a response body where every mapped slot
// carries what the mirror holds.
//
// The record is seeded THROUGH the pipeline (hubspot.Mapping → overlay.Apply)
// rather than hand-written in canonical shape, so the payload under test is
// the one production writes — a hand-built canonical fixture proves only that
// the wire reads the fixture's own author.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/modules/overlay/hubspot"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// personIncumbentFixture is one plausible HubSpot contact, in the INCUMBENT's
// own property vocabulary — the only vocabulary this gate hand-writes. Every
// property a mapped person binding names is present, and each value is
// distinguishable from what the wire assembles for a record that carries
// nothing, which is what lets a dropped pick be told apart from a fallback
// standing in for it.
func personIncumbentFixture() map[string]any {
	return map[string]any{
		"hs_object_id":     "100214862042",
		"firstname":        "Ada",
		"lastname":         "Overlay",
		"email":            "Ada@Example.DE",
		"jobtitle":         "CTO",
		"phone":            "+4930111",
		"mobilephone":      "+4917622",
		"address":          "Hauptstrasse 1",
		"city":             "Munich",
		"state":            "Bayern",
		"zip":              "80331",
		"country":          "DE",
		"createdate":       "2024-11-15T13:27:49.194Z",
		"lastmodifieddate": "2026-05-13T06:44:38.727Z",
	}
}

func TestEveryMappedPersonBindingReachesItsWireSlot(t *testing.T) {
	entity, ok := overlay.BindingsFor("person")
	if !ok {
		t.Fatal("the registry declares no person bindings; the source this gate derives from has moved")
	}
	canonical := personCanonicalFromMapping(t)
	filled := personWireBody(t, canonical)
	// The same assembly over a record carrying no mirrored field at all. Every
	// slot here is what the wire produces WITHOUT the mirror — a nil, a
	// placeholder name, the mirror's own sync instant — so a mapped slot that
	// still reads the same is a slot the mirror's value never reached, however
	// non-empty the fallback looks. The two records differ in nothing but their
	// payload and their generated identity, and no person slot is mapped from
	// the identity, so a difference can only have come from the mirror.
	unmirrored := personWireBody(t, map[string]any{})

	mapped := 0
	for _, b := range entity.Bindings {
		if b.Disposition != overlay.DispositionMapped {
			continue
		}
		mapped++
		if _, landed := canonical[b.CanonicalKey]; !landed {
			t.Errorf("person.%s is declared mapped from %v, but the mapping pipeline landed no %q in the "+
				"canonical payload. Either personIncumbentFixture omits those properties, or the HubSpot "+
				"mapping does not write that canonical key and the binding names one that never exists.",
				b.WireSlot, b.Incumbent, b.CanonicalKey)
			continue
		}
		value, present := filled[b.WireSlot]
		if !present || value == nil {
			t.Errorf("person.%s is declared mapped from %v, but the assembled body leaves it empty. "+
				"Either overlayWirePerson does not pick up %q, or the binding overstates what the mirror carries.",
				b.WireSlot, b.Incumbent, b.CanonicalKey)
			continue
		}
		if reflect.DeepEqual(value, unmirrored[b.WireSlot]) {
			t.Errorf("person.%s is declared mapped from %v, but the assembled body carries %v — the very value "+
				"a record with an EMPTY payload produces, so the mirror's %q never reached the caller. Have "+
				"overlayWirePerson read %q, or, if the value is genuinely the mirror's, give personIncumbentFixture "+
				"one that differs from the fallback.",
				b.WireSlot, b.Incumbent, value, b.CanonicalKey, b.CanonicalKey)
		}
	}
	if mapped == 0 {
		t.Fatal("person declares no mapped bindings; this gate would pass while checking nothing")
	}
}

// personCanonicalFromMapping projects the incumbent fixture through the real
// HubSpot contacts mapping, so the canonical payload under test is the one the
// ingest path writes. An unmapped property is a fixture typo — the mapping
// consumes every property the person bindings name — and it would otherwise
// read as a wire defect two assertions later.
func personCanonicalFromMapping(t *testing.T) map[string]any {
	t.Helper()
	m, ok := hubspot.Mapping("contacts")
	if !ok {
		t.Fatal("Mapping(contacts): want a declared mapping")
	}
	canonical, unmapped, err := overlay.Apply(m, personIncumbentFixture())
	if err != nil {
		t.Fatalf("Apply(contacts): %v", err)
	}
	if len(unmapped) != 0 {
		t.Fatalf("unmapped = %v: personIncumbentFixture names properties the contacts mapping does not consume, "+
			"so they reach no canonical key", unmapped)
	}
	return canonical
}

// personWireBody assembles a mirror record the way the REST surface serves it
// and renders the result as a client receives it.
func personWireBody(t *testing.T, fields map[string]any) map[string]any {
	t.Helper()
	person, err := overlayWirePerson(wireCtx(), wireRecord(t, datasource.EntityPerson, fields))
	if err != nil {
		t.Fatalf("overlayWirePerson: %v", err)
	}
	return marshalToMap(t, person)
}

// marshalToMap renders an assembled wire struct the way a client receives it.
// Asserting on the JSON rather than the Go struct is what makes an omitempty
// pointer left nil indistinguishable from a slot never filled — which is
// exactly the defect being watched for.
//
//craft:ignore naked-any v is any of the five assembled wire structs on their way through encoding/json — the untyped boundary itself
func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling the assembled wire struct: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding the assembled wire body: %v", err)
	}
	return body
}
